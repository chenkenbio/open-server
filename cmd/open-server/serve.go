package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"open-server/internal/filesystem"
	"open-server/internal/tensorboard"
	appweb "open-server/internal/web"
)

func runServe(configuration config, stderr io.Writer) error {
	root, initialPath, err := localServeRoot(configuration.targets[0])
	if err != nil {
		return err
	}

	token := configuration.token
	if token == "" {
		token, err = generateAccessToken()
		if err != nil {
			return fmt.Errorf("generate access token: %w", err)
		}
	}

	displayAddress := configuration.address
	bindAddress := "0.0.0.0"
	if displayAddress == "" {
		displayAddress = defaultServeAddress()
	} else {
		bindAddress = displayAddress
	}
	listener, err := listenServe(bindAddress, configuration.port)
	if err != nil {
		return fmt.Errorf("listen for serve mode: %w", err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	allowedHost := net.JoinHostPort(displayAddress, strconv.Itoa(port))

	hostname, err := os.Hostname()
	if err != nil || hostname == "" {
		hostname = displayAddress
	}
	title := configuration.title
	if title == "" {
		title = root
	}
	app, err := appweb.New(appweb.Options{
		Backend: filesystem.Local{}, Root: root, SSHHost: hostname,
		Title: title, AllowedHost: allowedHost, AccessToken: token,
		DefaultPython: configuration.python, LaTeX: configuration.latex,
		FontSize: configuration.fontSize,
	})
	if err != nil {
		return err
	}
	server := &http.Server{
		Handler:           app,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		ErrorLog:          log.New(io.Discard, "", 0),
	}
	serveResult := make(chan error, 1)
	go func() { serveResult <- server.Serve(listener) }()

	baseContext, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	ctx := baseContext
	cancel := func() {}
	if configuration.duration > 0 {
		ctx, cancel = context.WithTimeout(baseContext, configuration.duration)
	}
	defer cancel()

	fmt.Fprintln(stderr, "Open this network URL:", serveStartURL(app.URL(), initialPath))
	fmt.Fprintln(stderr, "Serving:", root)
	fmt.Fprintln(stderr, "Stop: Ctrl-C")
	printServeWarning(stderr)

	var result error
	select {
	case <-ctx.Done():
		reportContextEnd(ctx, stderr)
	case serveErr := <-serveResult:
		if !errors.Is(serveErr, http.ErrServerClosed) {
			result = fmt.Errorf("HTTP server stopped: %w", serveErr)
		}
	}

	shutdownContext, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := server.Shutdown(shutdownContext); err != nil && result == nil {
		result = fmt.Errorf("shut down HTTP server: %w", err)
	}
	return result
}

func localTensorBoardLauncher(configuration config, output io.Writer) (tensorboard.Launcher, func()) {
	if !configuration.tensorBoard {
		return nil, func() {}
	}
	launcher := tensorboard.NewLocal(configuration.python, output)
	return launcher, launcher.Close
}

func normalizeLocalTarget(name string) (string, error) {
	if name == "~" || strings.HasPrefix(name, "~/") || strings.HasPrefix(name, `~\`) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("find home directory: %w", err)
		}
		if name == "~" {
			name = home
		} else {
			name = filepath.Join(home, name[2:])
		}
	}
	absolute, err := filepath.Abs(name)
	if err != nil {
		return "", fmt.Errorf("resolve local path %q: %w", name, err)
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return "", fmt.Errorf("access local path %q: %w", name, err)
	}
	if !info.IsDir() && !info.Mode().IsRegular() {
		return "", fmt.Errorf("local path %q is not a regular file or directory", name)
	}
	return filepath.ToSlash(filepath.Clean(absolute)), nil
}

func localServeRoot(name string) (root, initialPath string, retErr error) {
	cleaned, err := normalizeLocalTarget(name)
	if err != nil {
		return "", "", err
	}
	info, err := os.Stat(filepath.FromSlash(cleaned))
	if err != nil {
		return "", "", fmt.Errorf("access local path %q: %w", name, err)
	}
	if info.IsDir() {
		return cleaned, "", nil
	}
	return filepath.ToSlash(filepath.Dir(filepath.FromSlash(cleaned))), cleaned, nil
}

func serveStartURL(appURL, initialPath string) string {
	if initialPath == "" {
		return appURL
	}
	parsed, err := url.Parse(appURL)
	if err != nil {
		return appURL
	}
	query := parsed.Query()
	query.Set("path", initialPath)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func generateAccessToken() (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(random[:]), nil
}

func defaultServeAddress() string {
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		return "127.0.0.1"
	}
	fallback := ""
	for _, address := range addresses {
		ip, _, err := net.ParseCIDR(address.String())
		if err != nil || ip.IsLoopback() || ip.To4() == nil {
			continue
		}
		if fallback == "" {
			fallback = ip.String()
		}
		if !isVirtualBridgeAddress(ip) {
			return ip.String()
		}
	}
	if fallback != "" {
		return fallback
	}
	return "127.0.0.1"
}

func isVirtualBridgeAddress(ip net.IP) bool {
	if ip.IsLinkLocalUnicast() {
		return true
	}
	for _, block := range []string{"172.16.0.0/12", "192.168.122.0/24"} {
		_, network, _ := net.ParseCIDR(block)
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

func listenServe(address string, port int) (net.Listener, error) {
	if port != 0 {
		return net.Listen("tcp4", net.JoinHostPort(address, strconv.Itoa(port)))
	}
	var lastErr error
	for candidate := automaticPortStart; candidate <= 65535; candidate++ {
		listener, err := net.Listen("tcp4", net.JoinHostPort(address, strconv.Itoa(candidate)))
		if err == nil {
			return listener, nil
		}
		if !errors.Is(err, syscall.EADDRINUSE) {
			return nil, err
		}
		lastErr = err
	}
	return nil, fmt.Errorf("no available port from %d through 65535: %w", automaticPortStart, lastErr)
}

func printServeWarning(output io.Writer) {
	fmt.Fprintln(output)
	fmt.Fprintln(output, "WARNING: serve mode uses plain, unencrypted HTTP.")
	fmt.Fprintln(output, "The access token limits who can connect, but URLs, file names, uploads, and downloads")
	fmt.Fprintln(output, "are not encrypted in transit. Use this mode only on a trusted network.")
}

func printLocalAppWarning(output io.Writer) {
	fmt.Fprintln(output)
	fmt.Fprintln(output, "WARNING: local app access is exposed through open-server's unauthenticated 127.0.0.1 listener.")
	fmt.Fprintln(output, "On a shared machine, any other local user can reach enabled TensorBoard or JupyterLab. Use an SSH target")
	fmt.Fprintln(output, "(open-server host:/path) so the service runs behind a private socket instead.")
}
