package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"remote-browser/internal/filesystem"
	"remote-browser/internal/sshsession"
	"remote-browser/internal/target"
	appweb "remote-browser/internal/web"
)

const version = "0.1.0"

const automaticPortStart = 60000

const defaultSessionDuration = 7 * 24 * time.Hour

type config struct {
	port     int
	rsh      string
	duration time.Duration
	title    string
	noOpen   bool
	version  bool
	target   string
}

func main() {
	if err := run(os.Args[1:], os.Stderr); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		fmt.Fprintln(os.Stderr, "remote-browser:", err)
		os.Exit(1)
	}
}

func run(arguments []string, stderr io.Writer) error {
	configuration, err := parseFlags(arguments, stderr)
	if err != nil {
		return err
	}
	if configuration.version {
		fmt.Fprintln(stderr, "remote-browser", version)
		return nil
	}
	remote, err := target.Parse(configuration.target)
	if err != nil {
		return err
	}

	baseContext, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	ctx := baseContext
	cancel := func() {}
	if configuration.duration > 0 {
		ctx, cancel = context.WithTimeout(baseContext, configuration.duration)
	}
	defer cancel()

	session, err := sshsession.Start(ctx, sshsession.Options{Executable: configuration.rsh, Host: remote.Host, Stderr: stderr})
	if err != nil {
		if ctx.Err() != nil {
			reportContextEnd(ctx, stderr)
			return nil
		}
		return err
	}
	defer session.Close()
	backend := filesystem.SFTP{Client: session.Client}
	root, err := logicalRemoteRoot(ctx, backend, remote.Path)
	if err != nil {
		if ctx.Err() != nil {
			reportContextEnd(ctx, stderr)
			return nil
		}
		return errors.New("the remote starting path could not be resolved")
	}
	info, err := backend.Stat(ctx, root)
	if err != nil {
		if ctx.Err() != nil {
			reportContextEnd(ctx, stderr)
			return nil
		}
		return errors.New("the remote starting path is not accessible")
	}
	if !info.IsDir() {
		return errors.New("the remote starting path is not a directory")
	}

	listener, err := listenLoopback(configuration.port)
	if err != nil {
		return fmt.Errorf("listen on IPv4 loopback: %w", err)
	}
	defer listener.Close()
	allowedHost := listener.Addr().String()
	app, err := appweb.New(appweb.Options{
		Backend: backend, Root: root, SSHHost: remote.Host,
		Title: configuration.title, AllowedHost: allowedHost,
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

	localURL := app.URL()
	launchContext, cancelLaunch := context.WithCancel(ctx)
	defer cancelLaunch()
	fmt.Fprintln(stderr, "Open this local URL:", localURL)
	var browserResult <-chan error
	if !configuration.noOpen {
		result := make(chan error, 1)
		browserResult = result
		go func() { result <- openBrowser(launchContext, localURL) }()
	}

	var result error
lifecycle:
	for {
		select {
		case browserErr := <-browserResult:
			if browserErr != nil {
				fmt.Fprintln(stderr, "Could not open a browser automatically.")
			} else {
				fmt.Fprintln(stderr, "Remote browser opened for", remote.Host+":"+root)
			}
			browserResult = nil
		case <-ctx.Done():
			reportContextEnd(ctx, stderr)
			break lifecycle
		case <-session.Done():
			if ctx.Err() == nil {
				result = errors.New("SSH connection closed unexpectedly")
			}
			break lifecycle
		case serveErr := <-serveResult:
			if !errors.Is(serveErr, http.ErrServerClosed) {
				result = fmt.Errorf("local HTTP server stopped: %w", serveErr)
			}
			break lifecycle
		}
	}
	cancelLaunch()

	shutdownContext, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := server.Shutdown(shutdownContext); err != nil && result == nil {
		result = fmt.Errorf("shut down local HTTP server: %w", err)
	}
	_ = session.Close()
	return result
}

func logicalRemoteRoot(ctx context.Context, backend filesystem.Backend, requested string) (string, error) {
	if path.IsAbs(requested) {
		return path.Clean(requested), nil
	}
	workingDirectory, err := backend.RealPath(ctx, ".")
	if err != nil {
		return "", err
	}
	if requested == "~" {
		return workingDirectory, nil
	}
	if strings.HasPrefix(requested, "~/") {
		return path.Join(workingDirectory, strings.TrimPrefix(requested, "~/")), nil
	}
	return path.Join(workingDirectory, requested), nil
}

func parseFlags(arguments []string, stderr io.Writer) (config, error) {
	var configuration config
	flags := flag.NewFlagSet("remote-browser", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.IntVar(&configuration.port, "port", 0, "local loopback port (0 scans from 60000)")
	flags.StringVar(&configuration.rsh, "rsh", "ssh", "OpenSSH executable or compatible wrapper")
	flags.DurationVar(&configuration.duration, "duration", defaultSessionDuration, "session duration (default 7d; for example 2h)")
	flags.StringVar(&configuration.title, "title", "", "browser page title")
	flags.BoolVar(&configuration.noOpen, "no-open", false, "print the URL instead of opening a browser")
	flags.BoolVar(&configuration.version, "version", false, "print the version and exit")
	flags.BoolVar(&configuration.version, "v", false, "print the version and exit")
	flags.Usage = func() {
		fmt.Fprintln(stderr, "Usage: remote-browser [options] host:/path")
		flags.PrintDefaults()
	}
	if err := flags.Parse(arguments); err != nil {
		return config{}, err
	}
	if configuration.version {
		return configuration, nil
	}
	if flags.NArg() != 1 {
		flags.Usage()
		return config{}, errors.New("exactly one remote target is required")
	}
	configuration.target = flags.Arg(0)
	if configuration.port < 0 || configuration.port > 65535 {
		return config{}, errors.New("port must be between 0 and 65535")
	}
	if configuration.duration < 0 {
		return config{}, errors.New("duration cannot be negative")
	}
	if configuration.rsh == "" {
		return config{}, errors.New("rsh executable cannot be empty")
	}
	return configuration, nil
}

func openBrowser(parent context.Context, address string) error {
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	var command *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		command = exec.CommandContext(ctx, "open", address)
	case "windows":
		command = exec.CommandContext(ctx, "cmd", "/c", "start", "", address)
	default:
		command = exec.CommandContext(ctx, "xdg-open", address)
	}
	return command.Run()
}

func listenLoopback(port int) (net.Listener, error) {
	if port != 0 {
		return listenLoopbackPort(port)
	}

	var lastErr error
	for candidate := automaticPortStart; candidate <= 65535; candidate++ {
		listener, err := listenLoopbackPort(candidate)
		if err == nil {
			return listener, nil
		}
		if !errors.Is(err, syscall.EADDRINUSE) {
			return nil, err
		}
		lastErr = err
	}
	return nil, fmt.Errorf("no available loopback port from %d through 65535: %w", automaticPortStart, lastErr)
}

func listenLoopbackPort(port int) (net.Listener, error) {
	return net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
}

func reportContextEnd(ctx context.Context, stderr io.Writer) {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		fmt.Fprintln(stderr, "Session duration expired; shutting down.")
	}
}
