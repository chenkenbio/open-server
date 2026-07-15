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

	"open-server/internal/filesystem"
	"open-server/internal/sessions"
	"open-server/internal/sshsession"
	"open-server/internal/target"
	"open-server/internal/tensorboard"
	appweb "open-server/internal/web"
)

const version = "0.1.1"

const automaticPortStart = 60000

const defaultSessionDuration = 7 * 24 * time.Hour

type config struct {
	port        int
	rsh         string
	duration    time.Duration
	title       string
	noOpen      bool
	serve       bool
	address     string
	token       string
	tensorBoard bool
	python      string
	latex       bool
	version     bool
	addName     string
	delete      string
	list        bool
	target      string
	explicit    map[string]bool
}

func main() {
	if err := run(os.Args[1:], os.Stderr); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		fmt.Fprintln(os.Stderr, "open-server:", err)
		os.Exit(1)
	}
}

func run(arguments []string, stderr io.Writer) error {
	configuration, err := parseFlags(arguments, stderr)
	if err != nil {
		return err
	}
	if configuration.version {
		fmt.Fprintln(stderr, "open-server", version)
		return nil
	}
	if configuration.addName != "" {
		store, err := sessions.DefaultStore()
		if err != nil {
			return err
		}
		options := savedSessionOptions(configuration)
		if err := store.Add(configuration.addName, configuration.target, options); err != nil {
			return err
		}
		fmt.Fprintf(stderr, "Saved session %q -> %s\n", configuration.addName, configuration.target)
		if text := formatSessionOptions(options); text != "" {
			fmt.Fprintln(stderr, "Options:", text)
		}
		fmt.Fprintln(stderr, "Config:", store.Path)
		return nil
	}
	if configuration.delete != "" {
		store, err := sessions.DefaultStore()
		if err != nil {
			return err
		}
		if err := store.Delete(configuration.delete); err != nil {
			if errors.Is(err, sessions.ErrNotFound) {
				return fmt.Errorf("saved session %q was not found", configuration.delete)
			}
			return err
		}
		fmt.Fprintf(stderr, "Deleted session %q\n", configuration.delete)
		return nil
	}
	if configuration.list {
		store, err := sessions.DefaultStore()
		if err != nil {
			return err
		}
		entries, err := store.List()
		if err != nil {
			return err
		}
		printSavedSessions(stderr, entries)
		return nil
	}
	if configuration.serve {
		return runServe(configuration, stderr)
	}
	remote, savedOptions, err := resolveRemoteTarget(configuration.target)
	if err != nil {
		return err
	}
	if err := applySavedSessionOptions(&configuration, savedOptions); err != nil {
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
	tensorBoardLauncher, closeTensorBoard := remoteTensorBoardLauncher(configuration, remote.Host, stderr)
	defer closeTensorBoard()
	app, err := appweb.New(appweb.Options{
		Backend: backend, Root: root, SSHHost: remote.Host,
		Title: configuration.title, AllowedHost: allowedHost,
		TensorBoard: tensorBoardLauncher, LaTeX: configuration.latex,
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
				fmt.Fprintln(stderr, "Open server session started for", remote.Host+":"+root)
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

func remoteTensorBoardLauncher(configuration config, host string, output io.Writer) (tensorboard.Launcher, func()) {
	if !configuration.tensorBoard {
		return nil, func() {}
	}
	launcher := tensorboard.NewRemote(configuration.rsh, host, configuration.python, output)
	return launcher, launcher.Close
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
	flags := flag.NewFlagSet("open-server", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.IntVar(&configuration.port, "port", 0, "HTTP port (0 scans from 60000)")
	flags.StringVar(&configuration.rsh, "rsh", "ssh", "OpenSSH executable or compatible wrapper")
	flags.DurationVar(&configuration.duration, "duration", defaultSessionDuration, "session duration (default 7d; for example 2h)")
	flags.StringVar(&configuration.title, "title", "", "browser page title")
	flags.BoolVar(&configuration.noOpen, "no-open", false, "do not open a browser in SSH/SFTP mode")
	flags.BoolVar(&configuration.serve, "serve", false, "serve a local path over token-protected plain HTTP")
	flags.StringVar(&configuration.address, "address", "", "reachable IPv4 address or hostname for serve mode (default auto-detected)")
	flags.StringVar(&configuration.token, "token", "", "access token for serve mode (minimum 8 characters; default random)")
	flags.BoolVar(&configuration.tensorBoard, "tensorboard", false, "show per-folder TensorBoard actions")
	flags.StringVar(&configuration.python, "python-interpreter", "", "Python interpreter containing TensorBoard")
	flags.StringVar(&configuration.python, "py", "", "Python interpreter containing TensorBoard (shorthand)")
	flags.BoolVar(&configuration.latex, "latex", false, "show LaTeX table, figure, and live-PDF actions")
	flags.StringVar(&configuration.addName, "add", "", "save or update a named session")
	flags.StringVar(&configuration.delete, "delete", "", "delete a named session")
	flags.BoolVar(&configuration.list, "list", false, "list saved sessions")
	flags.BoolVar(&configuration.version, "version", false, "print the version and exit")
	flags.BoolVar(&configuration.version, "v", false, "print the version and exit")
	flags.Usage = func() {
		fmt.Fprintln(stderr, "Usage:")
		fmt.Fprintln(stderr, "  open-server [options] host:/path")
		fmt.Fprintln(stderr, "  open-server [options] session-name")
		fmt.Fprintln(stderr, "  open-server -serve [options] [local-path]")
		fmt.Fprintln(stderr, "  open-server --add name host:/path")
		fmt.Fprintln(stderr, "  open-server --delete name")
		fmt.Fprintln(stderr, "  open-server --list")
		flags.PrintDefaults()
	}
	if err := flags.Parse(normalizeFlagOrder(arguments)); err != nil {
		return config{}, err
	}
	configuration.explicit = make(map[string]bool)
	flags.Visit(func(option *flag.Flag) { configuration.explicit[option.Name] = true })
	if configuration.version {
		return configuration, nil
	}
	operations := 0
	if configuration.addName != "" {
		operations++
	}
	if configuration.delete != "" {
		operations++
	}
	if configuration.list {
		operations++
	}
	if configuration.serve && operations != 0 {
		return config{}, errors.New("serve cannot be used with add, delete, or list")
	}
	if operations > 1 {
		return config{}, errors.New("add, delete, and list cannot be used together")
	}
	if configuration.serve {
		if flags.NArg() > 1 {
			flags.Usage()
			return config{}, errors.New("serve accepts at most one local path")
		}
		configuration.target = "."
		if flags.NArg() == 1 {
			configuration.target = flags.Arg(0)
		}
	} else if configuration.list || configuration.delete != "" {
		if flags.NArg() != 0 {
			flags.Usage()
			return config{}, errors.New("list and delete do not accept a remote target")
		}
	} else if flags.NArg() != 1 {
		flags.Usage()
		if configuration.addName != "" {
			return config{}, errors.New("add requires exactly one remote target")
		}
		return config{}, errors.New("exactly one remote target or saved session name is required")
	} else {
		configuration.target = flags.Arg(0)
	}
	if configuration.port < 0 || configuration.port > 65535 {
		return config{}, errors.New("port must be between 0 and 65535")
	}
	if configuration.duration < 0 {
		return config{}, errors.New("duration cannot be negative")
	}
	if configuration.rsh == "" {
		return config{}, errors.New("rsh executable cannot be empty")
	}
	if !configuration.serve && (configuration.address != "" || configuration.token != "") {
		return config{}, errors.New("address and token require serve mode")
	}
	if configuration.serve && configuration.token != "" && len(configuration.token) < 8 {
		return config{}, errors.New("token must be at least 8 characters")
	}
	return configuration, nil
}

func resolveRemoteTarget(value string) (target.Target, sessions.Options, error) {
	if strings.ContainsRune(value, ':') {
		remote, err := target.Parse(value)
		return remote, sessions.Options{}, err
	}
	store, err := sessions.DefaultStore()
	if err != nil {
		return target.Target{}, sessions.Options{}, err
	}
	saved, err := store.Resolve(value)
	if err != nil {
		if errors.Is(err, sessions.ErrNotFound) {
			return target.Target{}, sessions.Options{}, fmt.Errorf("saved session %q was not found; use --list to show saved sessions", value)
		}
		return target.Target{}, sessions.Options{}, err
	}
	remote, err := target.Parse(saved.Target)
	return remote, saved.Options, err
}

func printSavedSessions(output io.Writer, entries []sessions.Entry) {
	if len(entries) == 0 {
		fmt.Fprintln(output, "No saved sessions.")
		return
	}
	nameWidth := len("NAME")
	for _, entry := range entries {
		if len(entry.Name) > nameWidth {
			nameWidth = len(entry.Name)
		}
	}
	fmt.Fprintf(output, "%-*s  %-30s  %s\n", nameWidth, "NAME", "TARGET", "OPTIONS")
	for _, entry := range entries {
		fmt.Fprintf(output, "%-*s  %-30s  %s\n", nameWidth, entry.Name, entry.Target, formatSessionOptions(entry.Options))
	}
}

func normalizeFlagOrder(arguments []string) []string {
	valueFlags := map[string]bool{
		"-port": true, "--port": true, "-rsh": true, "--rsh": true,
		"-duration": true, "--duration": true, "-title": true, "--title": true,
		"-address": true, "--address": true, "-token": true, "--token": true,
		"-add": true, "--add": true, "-delete": true, "--delete": true,
		"-python-interpreter": true, "--python-interpreter": true, "-py": true,
	}
	var options, positional []string
	separator := false
	for index := 0; index < len(arguments); index++ {
		argument := arguments[index]
		if argument == "--" {
			positional = append(positional, arguments[index+1:]...)
			separator = true
			break
		}
		if strings.HasPrefix(argument, "-") && argument != "-" {
			options = append(options, argument)
			name := argument
			if equals := strings.IndexByte(name, '='); equals >= 0 {
				name = name[:equals]
			}
			if valueFlags[name] && !strings.ContainsRune(argument, '=') && index+1 < len(arguments) {
				index++
				options = append(options, arguments[index])
			}
			continue
		}
		positional = append(positional, argument)
	}
	if separator {
		options = append(options, "--")
	}
	return append(options, positional...)
}

func savedSessionOptions(configuration config) sessions.Options {
	var options sessions.Options
	if configuration.explicit["port"] {
		value := configuration.port
		options.Port = &value
	}
	if configuration.explicit["rsh"] {
		value := configuration.rsh
		options.RSH = &value
	}
	if configuration.explicit["duration"] {
		value := configuration.duration.String()
		options.Duration = &value
	}
	if configuration.explicit["title"] {
		value := configuration.title
		options.Title = &value
	}
	if configuration.explicit["no-open"] {
		value := configuration.noOpen
		options.NoOpen = &value
	}
	if configuration.explicit["tensorboard"] {
		value := configuration.tensorBoard
		options.TensorBoard = &value
	}
	if configuration.explicit["python-interpreter"] || configuration.explicit["py"] {
		value := configuration.python
		options.Python = &value
	}
	if configuration.explicit["latex"] {
		value := configuration.latex
		options.LaTeX = &value
	}
	return options
}

func applySavedSessionOptions(configuration *config, options sessions.Options) error {
	if options.Port != nil && !configuration.explicit["port"] {
		configuration.port = *options.Port
	}
	if options.RSH != nil && !configuration.explicit["rsh"] {
		configuration.rsh = *options.RSH
	}
	if options.Duration != nil && !configuration.explicit["duration"] {
		duration, err := time.ParseDuration(*options.Duration)
		if err != nil {
			return fmt.Errorf("invalid duration in saved session: %w", err)
		}
		configuration.duration = duration
	}
	if options.Title != nil && !configuration.explicit["title"] {
		configuration.title = *options.Title
	}
	if options.NoOpen != nil && !configuration.explicit["no-open"] {
		configuration.noOpen = *options.NoOpen
	}
	if options.TensorBoard != nil && !configuration.explicit["tensorboard"] {
		configuration.tensorBoard = *options.TensorBoard
	}
	if options.Python != nil && !configuration.explicit["python-interpreter"] && !configuration.explicit["py"] {
		configuration.python = *options.Python
	}
	if options.LaTeX != nil && !configuration.explicit["latex"] {
		configuration.latex = *options.LaTeX
	}
	return nil
}

func formatSessionOptions(options sessions.Options) string {
	var values []string
	if options.Port != nil {
		values = append(values, "-port="+strconv.Itoa(*options.Port))
	}
	if options.RSH != nil {
		values = append(values, "-rsh="+strconv.Quote(*options.RSH))
	}
	if options.Duration != nil {
		values = append(values, "-duration="+*options.Duration)
	}
	if options.Title != nil {
		values = append(values, "-title="+strconv.Quote(*options.Title))
	}
	if options.NoOpen != nil {
		values = append(values, "-no-open="+strconv.FormatBool(*options.NoOpen))
	}
	if options.TensorBoard != nil {
		values = append(values, "-tensorboard="+strconv.FormatBool(*options.TensorBoard))
	}
	if options.Python != nil {
		values = append(values, "-python-interpreter="+strconv.Quote(*options.Python))
	}
	if options.LaTeX != nil {
		values = append(values, "-latex="+strconv.FormatBool(*options.LaTeX))
	}
	return strings.Join(values, " ")
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
