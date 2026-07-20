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
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"open-server/internal/filesystem"
	"open-server/internal/jupyter"
	"open-server/internal/sessions"
	"open-server/internal/sshsession"
	"open-server/internal/target"
	"open-server/internal/tensorboard"
	appweb "open-server/internal/web"
)

const version = "0.2.1"

const automaticPortStart = 60000

const savedSessionPortStart = 61000

const defaultSessionDuration = 7 * 24 * time.Hour

var errSessionEndedDuringStartup = errors.New("session ended during startup")

type config struct {
	port        int
	rsh         string
	duration    time.Duration
	title       string
	fontSize    int
	noOpen      bool
	local       bool
	serve       bool
	address     string
	token       string
	tensorBoard bool
	jupyter     bool
	python      string
	latex       bool
	version     bool
	addName     string
	delete      string
	list        bool
	edit        bool
	targets     []string
	explicit    map[string]bool
}

type targetKind uint8

const (
	localTarget targetKind = iota
	remoteTarget
)

type resolvedTarget struct {
	label     string
	savedName string
	kind      targetKind
	local     string
	remote    target.Target
	options   sessions.Options
}

type runningSession struct {
	label  string
	done   <-chan error
	cancel context.CancelFunc
}

type synchronizedWriter struct {
	mu     sync.Mutex
	writer io.Writer
}

func (w *synchronizedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.writer.Write(p)
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
	if configuration.edit {
		store, err := sessions.DefaultStore()
		if err != nil {
			return err
		}
		return editSavedSessions(store, os.Stdin, os.Stdout, stderr)
	}
	stderr = &synchronizedWriter{writer: stderr}
	if configuration.addName != "" {
		resolved, err := resolveDirectTarget(configuration.targets[0], configuration.local)
		if err != nil {
			return err
		}
		targetValue := configuration.targets[0]
		if resolved.kind == localTarget {
			targetValue = resolved.local
		}
		store, err := sessions.DefaultStore()
		if err != nil {
			return err
		}
		options, err := savedSessionOptionsForAdd(store, configuration.addName, configuration)
		if err != nil {
			return err
		}
		if err := store.Add(configuration.addName, targetValue, options); err != nil {
			return err
		}
		fmt.Fprintf(stderr, "Saved session %q -> %s\n", configuration.addName, targetValue)
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
	return runSessions(configuration, stderr)
}

func runSessions(configuration config, stderr io.Writer) error {
	ctx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()

	type userCloseEvent struct {
		label       string
		requestedAt time.Time
	}
	userCloseEvents := make(chan userCloseEvent, 1)
	var userCloseOnce sync.Once
	requestUserClose := func(label string, requestedAt time.Time) {
		userCloseOnce.Do(func() {
			userCloseEvents <- userCloseEvent{label: label, requestedAt: requestedAt}
			stopSignals()
		})
	}

	type sessionEvent struct {
		session *runningSession
		err     error
	}
	events := make(chan sessionEvent, len(configuration.targets))
	active := make(map[*runningSession]struct{})
	var sessionErrors []error
	nextPort := 0

	for _, value := range configuration.targets {
		if ctx.Err() != nil {
			break
		}
		resolved, err := resolveTarget(value, configuration.local)
		if err != nil {
			err = fmt.Errorf("%s: %w", value, err)
			fmt.Fprintln(stderr, "Could not start session:", err)
			sessionErrors = append(sessionErrors, err)
			continue
		}
		sessionConfiguration, err := effectiveSessionConfig(configuration, resolved)
		if err != nil {
			err = fmt.Errorf("%s: %w", value, err)
			fmt.Fprintln(stderr, "Could not start session:", err)
			sessionErrors = append(sessionErrors, err)
			continue
		}

		reservedPorts, err := reservedSessionPorts(configuration, resolved)
		if err != nil {
			err = fmt.Errorf("%s: load saved port reservations: %w", value, err)
			fmt.Fprintln(stderr, "Could not start session:", err)
			sessionErrors = append(sessionErrors, err)
			continue
		}
		listener, assignedPort, err := listenSessionLoopback(configuration, sessionConfiguration, resolved, nextPort, reservedPorts)
		if err != nil {
			err = fmt.Errorf("%s: listen on IPv4 loopback: %w", value, err)
			fmt.Fprintln(stderr, "Could not start session:", err)
			sessionErrors = append(sessionErrors, err)
			continue
		}
		nextPort = assignedPort + 1

		running, localURL, err := startSession(ctx, requestUserClose, sessionConfiguration, resolved, listener, stderr)
		if err != nil {
			_ = listener.Close()
			if errors.Is(err, errSessionEndedDuringStartup) {
				continue
			}
			err = fmt.Errorf("%s: %w", value, err)
			fmt.Fprintln(stderr, "Could not start session:", err)
			sessionErrors = append(sessionErrors, err)
			continue
		}
		active[running] = struct{}{}
		if resolved.savedName != "" {
			if err := saveSessionPort(resolved.savedName, assignedPort); err != nil {
				fmt.Fprintf(stderr, "Could not save last port [%s]: %v\n", resolved.savedName, err)
			}
		}
		fmt.Fprintf(stderr, "Open this local URL [%s]: %s\n", value, localURL)
		if !sessionConfiguration.noOpen {
			go func(label, address string) {
				if err := openBrowser(address); err != nil && ctx.Err() == nil {
					fmt.Fprintf(stderr, "Could not open a browser automatically for %s.\n", label)
				}
			}(value, localURL)
		}
		go func(session *runningSession) {
			events <- sessionEvent{session: session, err: <-session.done}
		}(running)
	}

	if len(active) == 0 {
		if len(sessionErrors) == 0 && ctx.Err() != nil {
			return nil
		}
		return errors.Join(sessionErrors...)
	}

	for len(active) > 0 {
		select {
		case <-ctx.Done():
			for session := range active {
				session.cancel()
			}
			for len(active) > 0 {
				event := <-events
				delete(active, event.session)
				if event.err != nil {
					sessionErrors = append(sessionErrors, fmt.Errorf("%s: %w", event.session.label, event.err))
				}
			}
		case event := <-events:
			delete(active, event.session)
			if event.err != nil {
				err := fmt.Errorf("%s: %w", event.session.label, event.err)
				fmt.Fprintln(stderr, "Session stopped:", err)
				sessionErrors = append(sessionErrors, err)
			}
		}
	}
	select {
	case event := <-userCloseEvents:
		fmt.Fprintln(stderr, "Exit reason: user close")
		fmt.Fprintf(stderr, "Exit requested by [%s] at %s\n", event.label, event.requestedAt.Format(time.RFC3339))
		fmt.Fprintln(stderr, "Exit time:", time.Now().Format(time.RFC3339))
	default:
	}
	return errors.Join(sessionErrors...)
}

func effectiveSessionConfig(configuration config, resolved resolvedTarget) (config, error) {
	if resolved.kind == localTarget && !configuration.explicit["latex"] {
		configuration.latex = true
	}
	if err := applySavedSessionOptions(&configuration, resolved.options); err != nil {
		return config{}, err
	}
	return configuration, nil
}

func startSession(parent context.Context, requestUserClose func(string, time.Time), configuration config, resolved resolvedTarget, listener net.Listener, output io.Writer) (*runningSession, string, error) {
	ctx := parent
	cancel := func() {}
	if configuration.duration > 0 {
		ctx, cancel = context.WithTimeout(parent, configuration.duration)
	} else {
		ctx, cancel = context.WithCancel(parent)
	}

	backend := filesystem.Backend(filesystem.Local{})
	root := ""
	initialPath := ""
	host := ""
	var remoteDone <-chan struct{}
	closeBackend := func() error { return nil }
	var tensorBoardLauncher tensorboard.Launcher
	closeTensorBoard := func() {}
	var jupyterLauncher jupyter.Launcher
	closeJupyter := func() {}

	fail := func(err error) (*runningSession, string, error) {
		closeJupyter()
		closeTensorBoard()
		_ = closeBackend()
		cancel()
		return nil, "", err
	}
	endDuringStartup := func() (*runningSession, string, error) {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			fmt.Fprintf(output, "Session duration expired [%s]; shutting down.\n", resolved.label)
		}
		_, _, _ = fail(ctx.Err())
		return nil, "", errSessionEndedDuringStartup
	}

	if resolved.kind == localTarget {
		var err error
		root, initialPath, err = localServeRoot(resolved.local)
		if err != nil {
			return fail(err)
		}
		host, err = os.Hostname()
		if err != nil || host == "" {
			host = "localhost"
		}
		if configuration.title == "" {
			configuration.title = "Local files — " + root
		}
		tensorBoardLauncher, closeTensorBoard = localTensorBoardLauncher(configuration, output)
		jupyterLauncher, closeJupyter = localJupyterLauncher(configuration, output)
		if configuration.tensorBoard || configuration.jupyter {
			printLocalAppWarning(output)
		}
	} else {
		ssh, err := sshsession.Start(ctx, sshsession.Options{Executable: configuration.rsh, Host: resolved.remote.Host, Stderr: output})
		if err != nil {
			if ctx.Err() != nil {
				return endDuringStartup()
			}
			return fail(err)
		}
		closeBackend = ssh.Close
		remoteDone = ssh.Done()
		backend = filesystem.SFTP{Client: ssh.Client}
		root, err = logicalRemoteRoot(ctx, backend, resolved.remote.Path)
		if err != nil {
			if ctx.Err() != nil {
				return endDuringStartup()
			}
			return fail(errors.New("the remote starting path could not be resolved"))
		}
		info, err := backend.Stat(ctx, root)
		if err != nil {
			if ctx.Err() != nil {
				return endDuringStartup()
			}
			return fail(errors.New("the remote starting path is not accessible"))
		}
		if !info.IsDir() {
			return fail(errors.New("the remote starting path is not a directory"))
		}
		host = resolved.remote.Host
		tensorBoardLauncher, closeTensorBoard = remoteTensorBoardLauncher(configuration, host, output)
		jupyterLauncher, closeJupyter = remoteJupyterLauncher(configuration, host, output)
	}

	app, err := appweb.New(appweb.Options{
		Backend: backend, Root: root, SSHHost: host,
		Title: configuration.title, AllowedHost: listener.Addr().String(),
		Closeable:   true,
		TensorBoard: tensorBoardLauncher, Jupyter: jupyterLauncher,
		DefaultPython: configuration.python, LaTeX: configuration.latex,
		FontSize: configuration.fontSize,
	})
	if err != nil {
		return fail(err)
	}
	server := &http.Server{
		Handler:           app,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		ErrorLog:          log.New(io.Discard, "", 0),
	}
	serveResult := make(chan error, 1)
	go func() { serveResult <- server.Serve(listener) }()

	done := make(chan error, 1)
	running := &runningSession{label: resolved.label, done: done, cancel: cancel}
	go func() {
		var result error
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				fmt.Fprintf(output, "Session duration expired [%s]; shutting down.\n", resolved.label)
			}
		case requestedAt := <-app.CloseRequested():
			requestUserClose(resolved.label, requestedAt)
		case <-remoteDone:
			if ctx.Err() == nil {
				result = errors.New("SSH connection closed unexpectedly")
			}
		case serveErr := <-serveResult:
			if !errors.Is(serveErr, http.ErrServerClosed) {
				result = fmt.Errorf("local HTTP server stopped: %w", serveErr)
			}
		}
		cancel()
		shutdownContext, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := server.Shutdown(shutdownContext); err != nil && result == nil {
			result = fmt.Errorf("shut down local HTTP server: %w", err)
		}
		shutdownCancel()
		closeJupyter()
		closeTensorBoard()
		_ = closeBackend()
		_ = listener.Close()
		done <- result
	}()

	return running, serveStartURL(app.URL(), initialPath), nil
}

func remoteTensorBoardLauncher(configuration config, host string, output io.Writer) (tensorboard.Launcher, func()) {
	if !configuration.tensorBoard {
		return nil, func() {}
	}
	launcher := tensorboard.NewRemote(configuration.rsh, host, configuration.python, output)
	return launcher, launcher.Close
}

func localJupyterLauncher(configuration config, output io.Writer) (jupyter.Launcher, func()) {
	if !configuration.jupyter {
		return nil, func() {}
	}
	launcher := jupyter.NewLocal(configuration.python, output)
	return launcher, launcher.Close
}

func remoteJupyterLauncher(configuration config, host string, output io.Writer) (jupyter.Launcher, func()) {
	if !configuration.jupyter {
		return nil, func() {}
	}
	launcher := jupyter.NewRemote(configuration.rsh, host, configuration.python, output)
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
	flags.IntVar(&configuration.port, "port", 0, "starting HTTP port (direct targets default to 60000; saved sessions remember theirs)")
	flags.StringVar(&configuration.rsh, "rsh", "ssh", "OpenSSH executable or compatible wrapper")
	flags.DurationVar(&configuration.duration, "duration", defaultSessionDuration, "session duration (default 7d; for example 2h)")
	flags.StringVar(&configuration.title, "title", "", "browser page title")
	flags.IntVar(&configuration.fontSize, "fontsize", appweb.DefaultFontSize, fmt.Sprintf("file browser font size in pixels (%d-%d)", appweb.MinFontSize, appweb.MaxFontSize))
	flags.BoolVar(&configuration.noOpen, "no-open", false, "do not open a browser automatically")
	flags.BoolVar(&configuration.local, "local", false, "interpret every target as a local path")
	flags.BoolVar(&configuration.serve, "serve", false, "expose this machine's path over token-protected plain HTTP")
	flags.StringVar(&configuration.address, "address", "", "reachable IPv4 address or hostname for serve mode (default auto-detected)")
	flags.StringVar(&configuration.token, "token", "", "access token for serve mode (minimum 8 characters; default random)")
	flags.BoolVar(&configuration.tensorBoard, "tensorboard", false, "show TensorBoard launch actions for event-log folders")
	flags.BoolVar(&configuration.jupyter, "jupyter", false, "show JupyterLab launch actions for folders")
	flags.StringVar(&configuration.python, "python-interpreter", "", "Python interpreter containing TensorBoard and JupyterLab")
	flags.StringVar(&configuration.python, "py", "", "Python interpreter containing TensorBoard and JupyterLab (shorthand)")
	flags.BoolVar(&configuration.latex, "latex", false, "show LaTeX table, figure, and live-PDF actions (default for local targets)")
	flags.StringVar(&configuration.addName, "add", "", "save or update a named session")
	flags.StringVar(&configuration.delete, "delete", "", "delete a named session")
	flags.BoolVar(&configuration.list, "list", false, "list saved sessions")
	flags.BoolVar(&configuration.edit, "edit", false, "edit the saved sessions config")
	flags.BoolVar(&configuration.version, "version", false, "print the version and exit")
	flags.BoolVar(&configuration.version, "v", false, "print the version and exit")
	flags.Usage = func() {
		fmt.Fprintln(stderr, "Usage:")
		fmt.Fprintln(stderr, "  open-server [options] target [target ...]")
		fmt.Fprintln(stderr, "  open-server -serve [options] [local-path]")
		fmt.Fprintln(stderr, "  open-server --add name target")
		fmt.Fprintln(stderr, "  open-server --delete name")
		fmt.Fprintln(stderr, "  open-server --list")
		fmt.Fprintln(stderr, "  open-server --edit")
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
	if configuration.edit {
		operations++
	}
	if configuration.serve && operations != 0 {
		return config{}, errors.New("serve cannot be used with add, delete, list, or edit")
	}
	if configuration.serve && configuration.local {
		return config{}, errors.New("local cannot be used with serve")
	}
	if configuration.serve && configuration.jupyter {
		return config{}, errors.New("jupyter cannot be used with serve; use a local or SSH/SFTP session")
	}
	if configuration.serve && configuration.tensorBoard {
		return config{}, errors.New("tensorboard cannot be used with serve; use a local or SSH/SFTP session")
	}
	if operations > 1 {
		return config{}, errors.New("add, delete, list, and edit cannot be used together")
	}
	if configuration.serve {
		if flags.NArg() > 1 {
			flags.Usage()
			return config{}, errors.New("serve accepts at most one local path")
		}
		configuration.targets = []string{"."}
		if flags.NArg() == 1 {
			configuration.targets[0] = flags.Arg(0)
		}
	} else if configuration.list || configuration.delete != "" || configuration.edit {
		if configuration.local {
			return config{}, errors.New("local cannot be used with list, delete, or edit")
		}
		if flags.NArg() != 0 {
			flags.Usage()
			return config{}, errors.New("list, delete, and edit do not accept targets")
		}
	} else if configuration.addName != "" {
		if flags.NArg() != 1 {
			flags.Usage()
			return config{}, errors.New("add requires exactly one target")
		}
		configuration.targets = flags.Args()
	} else if flags.NArg() == 0 {
		flags.Usage()
		return config{}, errors.New("at least one local path, remote target, or saved session name is required")
	} else {
		configuration.targets = flags.Args()
	}
	if configuration.port < 0 || configuration.port > 65535 {
		return config{}, errors.New("port must be between 0 and 65535")
	}
	if configuration.duration < 0 {
		return config{}, errors.New("duration cannot be negative")
	}
	if configuration.fontSize < appweb.MinFontSize || configuration.fontSize > appweb.MaxFontSize {
		return config{}, fmt.Errorf("fontsize must be between %d and %d", appweb.MinFontSize, appweb.MaxFontSize)
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

func resolveTarget(value string, forceLocal bool) (resolvedTarget, error) {
	resolved, direct, err := parseDirectTarget(value, forceLocal)
	if err != nil || direct {
		return resolved, err
	}
	store, err := sessions.DefaultStore()
	if err != nil {
		return resolvedTarget{}, err
	}
	saved, err := store.Resolve(value)
	if err != nil {
		if errors.Is(err, sessions.ErrNotFound) {
			return resolvedTarget{}, fmt.Errorf("saved session %q was not found; use --list to show saved sessions or -local to open a bare local name", value)
		}
		return resolvedTarget{}, err
	}
	resolved, direct, err = parseDirectTarget(saved.Target, false)
	if err != nil {
		return resolvedTarget{}, err
	}
	if !direct {
		return resolvedTarget{}, errors.New("saved session has an invalid target")
	}
	resolved.label = value
	resolved.savedName = value
	resolved.options = saved.Options
	return resolved, nil
}

func resolveDirectTarget(value string, forceLocal bool) (resolvedTarget, error) {
	resolved, direct, err := parseDirectTarget(value, forceLocal)
	if err != nil {
		return resolvedTarget{}, err
	}
	if !direct {
		return resolvedTarget{}, errors.New("target must be a local path or have the form host:/path")
	}
	return resolved, nil
}

func parseDirectTarget(value string, forceLocal bool) (resolvedTarget, bool, error) {
	resolved := resolvedTarget{label: value}
	localSyntax := forceLocal || isAbsoluteLocalTarget(value) || value == "." || value == ".." || value == "~" ||
		strings.HasPrefix(value, "./") || strings.HasPrefix(value, "../") || strings.HasPrefix(value, "~/") ||
		strings.HasPrefix(value, `.\`) || strings.HasPrefix(value, `..\`) || strings.HasPrefix(value, `~\`)
	if localSyntax {
		localName, err := normalizeLocalTarget(value)
		if err != nil {
			return resolvedTarget{}, true, err
		}
		resolved.kind = localTarget
		resolved.local = localName
		return resolved, true, nil
	}
	if strings.ContainsRune(value, ':') {
		remote, err := target.Parse(value)
		if err != nil {
			return resolvedTarget{}, true, err
		}
		resolved.kind = remoteTarget
		resolved.remote = remote
		return resolved, true, nil
	}
	if strings.ContainsAny(value, `/\`) {
		localName, err := normalizeLocalTarget(value)
		if err != nil {
			return resolvedTarget{}, true, err
		}
		resolved.kind = localTarget
		resolved.local = localName
		return resolved, true, nil
	}
	return resolved, false, nil
}

func isAbsoluteLocalTarget(value string) bool {
	return filepath.IsAbs(filepath.FromSlash(value))
}

func resolveRemoteTarget(value string) (target.Target, sessions.Options, error) {
	resolved, err := resolveTarget(value, false)
	if err != nil {
		return target.Target{}, sessions.Options{}, err
	}
	if resolved.kind != remoteTarget {
		return target.Target{}, sessions.Options{}, errors.New("target is local, not remote")
	}
	return resolved.remote, resolved.options, nil
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
		"-fontsize": true, "--fontsize": true,
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
	if configuration.explicit["fontsize"] {
		value := configuration.fontSize
		options.FontSize = &value
	}
	if configuration.explicit["no-open"] {
		value := configuration.noOpen
		options.NoOpen = &value
	}
	if configuration.explicit["tensorboard"] {
		value := configuration.tensorBoard
		options.TensorBoard = &value
	}
	if configuration.explicit["jupyter"] {
		value := configuration.jupyter
		options.Jupyter = &value
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

func savedSessionOptionsForAdd(store sessions.Store, name string, configuration config) (sessions.Options, error) {
	options := savedSessionOptions(configuration)
	if options.Port != nil {
		return options, nil
	}
	existing, err := store.Resolve(name)
	if err == nil && existing.Options.Port != nil {
		value := *existing.Options.Port
		options.Port = &value
		return options, nil
	}
	if err != nil && !errors.Is(err, sessions.ErrNotFound) {
		return sessions.Options{}, err
	}
	reserved, err := store.ReservedPorts(name)
	if err != nil {
		return sessions.Options{}, err
	}
	port, err := firstUnreservedPort(savedSessionPortStart, reserved)
	if err != nil {
		return sessions.Options{}, err
	}
	options.Port = &port
	return options, nil
}

func firstUnreservedPort(start int, reserved map[int]struct{}) (int, error) {
	for candidate := start; candidate <= 65535; candidate++ {
		if _, exists := reserved[candidate]; !exists {
			return candidate, nil
		}
	}
	return 0, fmt.Errorf("no unreserved saved-session port from %d through 65535", start)
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
	if options.FontSize != nil && !configuration.explicit["fontsize"] {
		configuration.fontSize = *options.FontSize
	}
	if options.NoOpen != nil && !configuration.explicit["no-open"] {
		configuration.noOpen = *options.NoOpen
	}
	if options.TensorBoard != nil && !configuration.explicit["tensorboard"] {
		configuration.tensorBoard = *options.TensorBoard
	}
	if options.Jupyter != nil && !configuration.explicit["jupyter"] {
		configuration.jupyter = *options.Jupyter
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
	if options.FontSize != nil {
		values = append(values, "-fontsize="+strconv.Itoa(*options.FontSize))
	}
	if options.NoOpen != nil {
		values = append(values, "-no-open="+strconv.FormatBool(*options.NoOpen))
	}
	if options.TensorBoard != nil {
		values = append(values, "-tensorboard="+strconv.FormatBool(*options.TensorBoard))
	}
	if options.Jupyter != nil {
		values = append(values, "-jupyter="+strconv.FormatBool(*options.Jupyter))
	}
	if options.Python != nil {
		values = append(values, "-python-interpreter="+strconv.Quote(*options.Python))
	}
	if options.LaTeX != nil {
		values = append(values, "-latex="+strconv.FormatBool(*options.LaTeX))
	}
	return strings.Join(values, " ")
}

func openBrowser(address string) error {
	var command *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		command = exec.Command("open", address)
	case "windows":
		command = exec.Command("cmd", "/c", "start", "", address)
	default:
		command = exec.Command("xdg-open", address)
	}
	configureBrowserCommand(command)
	return command.Run()
}

func listenLoopback(port int) (net.Listener, error) {
	start := port
	if start == 0 {
		start = automaticPortStart
	}
	listener, _, err := listenLoopbackFrom(start)
	return listener, err
}

func listenSessionLoopback(invocation, effective config, resolved resolvedTarget, nextPort int, reserved map[int]struct{}) (net.Listener, int, error) {
	if resolved.savedName != "" && !invocation.explicit["port"] {
		if resolved.options.Port != nil && *resolved.options.Port > 0 {
			return listenLoopbackPreferred(*resolved.options.Port, savedSessionPortStart, reserved)
		}
		start := savedSessionPortStart
		if nextPort > start {
			start = nextPort
		}
		return listenLoopbackFromSkipping(start, reserved)
	}
	start := effective.port
	if start == 0 {
		start = automaticPortStart
	}
	if nextPort > start {
		start = nextPort
	}
	return listenLoopbackFrom(start)
}

func listenLoopbackPreferred(preferred, fallbackStart int, reserved map[int]struct{}) (net.Listener, int, error) {
	if _, otherSessionOwnsPort := reserved[preferred]; !otherSessionOwnsPort {
		listener, err := listenLoopbackPort(preferred)
		if err == nil {
			return listener, preferred, nil
		}
		if !errors.Is(err, syscall.EADDRINUSE) {
			return nil, 0, err
		}
	}
	return listenLoopbackFromSkipping(fallbackStart, reserved)
}

func reservedSessionPorts(invocation config, resolved resolvedTarget) (map[int]struct{}, error) {
	if resolved.savedName == "" || invocation.explicit["port"] {
		return nil, nil
	}
	store, err := sessions.DefaultStore()
	if err != nil {
		return nil, err
	}
	return store.ReservedPorts(resolved.savedName)
}

func saveSessionPort(name string, port int) error {
	store, err := sessions.DefaultStore()
	if err != nil {
		return err
	}
	return store.UpdatePort(name, port)
}

func listenLoopbackFrom(start int) (net.Listener, int, error) {
	return listenLoopbackFromSkipping(start, nil)
}

func listenLoopbackFromSkipping(start int, reserved map[int]struct{}) (net.Listener, int, error) {
	var lastErr error
	for candidate := start; candidate <= 65535; candidate++ {
		if _, exists := reserved[candidate]; exists {
			lastErr = errors.New("port is reserved by another saved session")
			continue
		}
		listener, err := listenLoopbackPort(candidate)
		if err == nil {
			return listener, candidate, nil
		}
		if !errors.Is(err, syscall.EADDRINUSE) {
			return nil, 0, err
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = errors.New("starting port exceeds 65535")
	}
	return nil, 0, fmt.Errorf("no available loopback port from %d through 65535: %w", start, lastErr)
}

func listenLoopbackPort(port int) (net.Listener, error) {
	return net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
}

func reportContextEnd(ctx context.Context, stderr io.Writer) {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		fmt.Fprintln(stderr, "Session duration expired; shutting down.")
	}
}
