package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/pkg/sftp"

	"open-server/internal/filesystem"
	"open-server/internal/sessions"
)

func TestParseFlags(t *testing.T) {
	t.Parallel()
	defaults, err := parseFlags([]string{"lab:/tmp"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if defaults.duration != defaultSessionDuration {
		t.Fatalf("default duration = %s, want %s", defaults.duration, defaultSessionDuration)
	}

	configuration, err := parseFlags([]string{"--port", "8123", "--duration", "90m", "--title", "Files", "lab:/tmp", "--tensorboard", "--python-interpreter", "/env/bin/python", "--latex"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if configuration.port != 8123 || configuration.duration != 90*time.Minute || configuration.title != "Files" || len(configuration.targets) != 1 || configuration.targets[0] != "lab:/tmp" || !configuration.tensorBoard || configuration.python != "/env/bin/python" || !configuration.latex {
		t.Fatalf("configuration = %#v", configuration)
	}
	add, err := parseFlags([]string{"--add", "work", "lab:/srv/work"}, io.Discard)
	if err != nil || add.addName != "work" || len(add.targets) != 1 || add.targets[0] != "lab:/srv/work" {
		t.Fatalf("add configuration = %#v, %v", add, err)
	}
	list, err := parseFlags([]string{"-list"}, io.Discard)
	if err != nil || !list.list {
		t.Fatalf("list configuration = %#v, %v", list, err)
	}
	remove, err := parseFlags([]string{"-delete", "work"}, io.Discard)
	if err != nil || remove.delete != "work" {
		t.Fatalf("delete configuration = %#v, %v", remove, err)
	}
	edit, err := parseFlags([]string{"-edit"}, io.Discard)
	if err != nil || !edit.edit {
		t.Fatalf("edit configuration = %#v, %v", edit, err)
	}
	serve, err := parseFlags([]string{"-serve"}, io.Discard)
	if err != nil || !serve.serve || len(serve.targets) != 1 || serve.targets[0] != "." {
		t.Fatalf("serve configuration = %#v, %v", serve, err)
	}
	multiple, err := parseFlags([]string{"session-one", "lab:/two", "./local"}, io.Discard)
	if err != nil || len(multiple.targets) != 3 {
		t.Fatalf("multiple-target configuration = %#v, %v", multiple, err)
	}
	for _, arguments := range [][]string{
		{}, {"--port", "70000", "lab:/tmp"}, {"--duration", "-1s", "lab:/tmp"},
		{"--add", "work"}, {"--add", "work", "lab:/one", "lab:/two"}, {"--list", "lab:/tmp"},
		{"--delete", "work", "lab:/tmp"}, {"--add", "work", "--list", "lab:/tmp"},
		{"--serve", "--local", "."}, {"--list", "--local"}, {"--edit", "lab:/tmp"},
		{"--edit", "--list"}, {"--edit", "--add", "work", "lab:/tmp"}, {"--edit", "--serve"},
		{"--edit", "--local"},
	} {
		if _, err := parseFlags(arguments, io.Discard); err == nil {
			t.Errorf("parseFlags(%q) succeeded, want error", arguments)
		}
	}
}

func TestSavedSessionCommands(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("HOME", configHome)

	var addOutput bytes.Buffer
	if err := run([]string{"--add", "work", "lab:/srv/work", "--tensorboard", "-py", "/env/bin/python", "--latex", "--title", "Dashboards"}, &addOutput); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(addOutput.String(), `Saved session "work" -> lab:/srv/work`) {
		t.Fatalf("add output = %q", addOutput.String())
	}
	if !strings.Contains(addOutput.String(), "-port=61000") || !strings.Contains(addOutput.String(), "-tensorboard=true") || !strings.Contains(addOutput.String(), `-python-interpreter="/env/bin/python"`) || !strings.Contains(addOutput.String(), "-latex=true") {
		t.Fatalf("add output does not show saved options: %q", addOutput.String())
	}

	var listOutput bytes.Buffer
	if err := run([]string{"--list"}, &listOutput); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(listOutput.String(), "NAME  TARGET") || !strings.Contains(listOutput.String(), "work  lab:/srv/work") {
		t.Fatalf("list output = %q", listOutput.String())
	}

	resolved, options, err := resolveRemoteTarget("work")
	if err != nil || resolved.Host != "lab" || resolved.Path != "/srv/work" {
		t.Fatalf("resolveRemoteTarget(work) = %#v, %v", resolved, err)
	}
	if options.Port == nil || *options.Port != savedSessionPortStart || options.TensorBoard == nil || !*options.TensorBoard || options.Python == nil || *options.Python != "/env/bin/python" || options.LaTeX == nil || !*options.LaTeX || options.Title == nil || *options.Title != "Dashboards" {
		t.Fatalf("saved options = %#v", options)
	}
	invocation, err := parseFlags([]string{"work", "--tensorboard=false", "--latex=false"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if err := applySavedSessionOptions(&invocation, options); err != nil {
		t.Fatal(err)
	}
	if invocation.tensorBoard || invocation.python != "/env/bin/python" || invocation.latex || invocation.title != "Dashboards" {
		t.Fatalf("merged saved options = %#v", invocation)
	}
	if _, _, err := resolveRemoteTarget("missing"); err == nil || !strings.Contains(err.Error(), "use --list") {
		t.Fatalf("resolveRemoteTarget(missing) error = %v", err)
	}

	localDirectory := t.TempDir()
	var localAddOutput bytes.Buffer
	if err := run([]string{"--add", "local-paper", "--local", localDirectory, "--latex=false"}, &localAddOutput); err != nil {
		t.Fatal(err)
	}
	localSaved, err := resolveTarget("local-paper", false)
	if err != nil || localSaved.savedName != "local-paper" || localSaved.kind != localTarget || localSaved.local != filepath.ToSlash(localDirectory) {
		t.Fatalf("resolveTarget(local-paper) = %#v, %v", localSaved, err)
	}
	if localSaved.options.LaTeX == nil || *localSaved.options.LaTeX {
		t.Fatalf("saved local options = %#v", localSaved.options)
	}
	if localSaved.options.Port == nil || *localSaved.options.Port != savedSessionPortStart+1 {
		t.Fatalf("second saved session port = %#v, want %d", localSaved.options.Port, savedSessionPortStart+1)
	}
	localPort := *localSaved.options.Port
	if err := run([]string{"--add", "local-paper", "--local", localDirectory, "--title", "Updated paper"}, io.Discard); err != nil {
		t.Fatal(err)
	}
	localSaved, err = resolveTarget("local-paper", false)
	if err != nil || localSaved.options.Port == nil || *localSaved.options.Port != localPort {
		t.Fatalf("updated saved session port = %#v, %v; want preserved %d", localSaved.options.Port, err, localPort)
	}

	var deleteOutput bytes.Buffer
	if err := run([]string{"--delete", "work"}, &deleteOutput); err != nil {
		t.Fatal(err)
	}
	if got, want := deleteOutput.String(), "Deleted session \"work\"\n"; got != want {
		t.Fatalf("delete output = %q, want %q", got, want)
	}
	if err := run([]string{"--delete", "work"}, io.Discard); err == nil || !strings.Contains(err.Error(), "was not found") {
		t.Fatalf("second delete error = %v", err)
	}
}

func TestTensorBoardLauncherIsNilWhenDisabled(t *testing.T) {
	configuration := config{rsh: "ssh"}
	remoteLauncher, closeRemote := remoteTensorBoardLauncher(configuration, "lab", io.Discard)
	defer closeRemote()
	if remoteLauncher != nil {
		t.Fatalf("disabled remote TensorBoard launcher = %#v, want nil", remoteLauncher)
	}
	localLauncher, closeLocal := localTensorBoardLauncher(configuration, io.Discard)
	defer closeLocal()
	if localLauncher != nil {
		t.Fatalf("disabled local TensorBoard launcher = %#v, want nil", localLauncher)
	}

	configuration.tensorBoard = true
	remoteLauncher, closeRemote = remoteTensorBoardLauncher(configuration, "lab", io.Discard)
	defer closeRemote()
	localLauncher, closeLocal = localTensorBoardLauncher(configuration, io.Discard)
	defer closeLocal()
	if remoteLauncher == nil || localLauncher == nil {
		t.Fatal("enabled TensorBoard launcher is nil")
	}
}

func TestLocalServeRoot(t *testing.T) {
	directory := t.TempDir()
	root, initialPath, err := localServeRoot(directory)
	if err != nil || root != filepath.ToSlash(directory) || initialPath != "" {
		t.Fatalf("localServeRoot(%q) = %q, %q, %v", directory, root, initialPath, err)
	}
	fileName := filepath.Join(directory, "paper.pdf")
	if err := os.WriteFile(fileName, []byte("%PDF-1.4\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, initialPath, err = localServeRoot(fileName)
	if err != nil || root != filepath.ToSlash(directory) || initialPath != filepath.ToSlash(fileName) {
		t.Fatalf("localServeRoot(%q) = %q, %q, %v", fileName, root, initialPath, err)
	}
	if got := serveStartURL("http://127.0.0.1:60000/?token=secret-token", initialPath); !strings.Contains(got, "path=") || !strings.Contains(got, "token=secret-token") {
		t.Fatalf("serveStartURL = %q", got)
	}
	if _, _, err := localServeRoot(filepath.Join(directory, "missing")); err == nil {
		t.Fatal("localServeRoot accepted a missing path")
	}
	token, err := generateAccessToken()
	if err != nil || len(token) != 32 {
		t.Fatalf("generated token = %q, %v", token, err)
	}
}

func TestLocalTargetResolutionAndDefaults(t *testing.T) {
	directory := t.TempDir()
	resolved, err := resolveDirectTarget(directory, false)
	if err != nil || resolved.kind != localTarget || resolved.local != filepath.ToSlash(directory) {
		t.Fatalf("resolveDirectTarget(%q) = %#v, %v", directory, resolved, err)
	}
	if _, direct, err := parseDirectTarget("bare-name", false); err != nil || direct {
		t.Fatalf("bare target direct = %v, %v; want saved-session lookup", direct, err)
	}
	if _, err := resolveDirectTarget("missing-bare-name", true); err == nil || !strings.Contains(err.Error(), "access local path") {
		t.Fatalf("forced missing local error = %v", err)
	}

	base := config{explicit: make(map[string]bool)}
	effective, err := effectiveSessionConfig(base, resolvedTarget{kind: localTarget})
	if err != nil || !effective.latex {
		t.Fatalf("local default configuration = %#v, %v", effective, err)
	}
	latex := false
	effective, err = effectiveSessionConfig(base, resolvedTarget{kind: localTarget, options: sessions.Options{LaTeX: &latex}})
	if err != nil || effective.latex {
		t.Fatalf("saved local LaTeX override = %#v, %v", effective, err)
	}
	explicit := config{latex: false, explicit: map[string]bool{"latex": true}}
	effective, err = effectiveSessionConfig(explicit, resolvedTarget{kind: localTarget})
	if err != nil || effective.latex {
		t.Fatalf("explicit local LaTeX override = %#v, %v", effective, err)
	}
}

func TestRunMultipleLocalSessionsIsolatesFailureAndAllocatesPorts(t *testing.T) {
	first := t.TempDir()
	second := t.TempDir()
	missing := filepath.Join(t.TempDir(), "missing")
	output := newNotifyWriter("never-ready")
	err := run([]string{"--duration", "100ms", "--no-open", first, missing, second}, output)
	if err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("multi-session error = %v", err)
	}
	matches := regexp.MustCompile(`http://127\.0\.0\.1:(\d+)/`).FindAllStringSubmatch(output.String(), -1)
	if len(matches) != 2 {
		t.Fatalf("session output has %d URLs, want 2: %s", len(matches), output.String())
	}
	firstPort, _ := strconv.Atoi(matches[0][1])
	secondPort, _ := strconv.Atoi(matches[1][1])
	if secondPort <= firstPort {
		t.Fatalf("allocated ports = %d, %d; want increasing", firstPort, secondPort)
	}
}

func TestSavedSessionRemembersLastSuccessfulPort(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("HOME", configHome)
	root := t.TempDir()
	if err := run([]string{"--add", "remember-port", "--local", root}, io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"--add", "reserved-port", "--local", t.TempDir()}, io.Discard); err != nil {
		t.Fatal(err)
	}

	launch := func() int {
		t.Helper()
		var output bytes.Buffer
		if err := run([]string{"--duration", "100ms", "--no-open", "remember-port"}, &output); err != nil {
			t.Fatal(err)
		}
		match := regexp.MustCompile(`http://127\.0\.0\.1:(\d+)/`).FindStringSubmatch(output.String())
		if len(match) != 2 {
			t.Fatalf("session output has no local URL: %s", output.String())
		}
		port, err := strconv.Atoi(match[1])
		if err != nil {
			t.Fatal(err)
		}
		return port
	}

	firstPort := launch()
	if firstPort < savedSessionPortStart {
		t.Fatalf("first saved-session port = %d, want at least %d", firstPort, savedSessionPortStart)
	}
	store, err := sessions.DefaultStore()
	if err != nil {
		t.Fatal(err)
	}
	saved, err := store.Resolve("remember-port")
	if err != nil || saved.Options.Port == nil || *saved.Options.Port != firstPort {
		t.Fatalf("saved port after first launch = %#v, %v; want %d", saved.Options.Port, err, firstPort)
	}
	if secondPort := launch(); secondPort != firstPort {
		t.Fatalf("reused saved-session port = %d, want %d", secondPort, firstPort)
	}

	occupied, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	occupiedPort := occupied.Addr().(*net.TCPAddr).Port
	if err := store.UpdatePort("remember-port", occupiedPort); err != nil {
		t.Fatal(err)
	}
	reserved, err := store.Resolve("reserved-port")
	if err != nil || reserved.Options.Port == nil {
		t.Fatalf("reserved session = %#v, %v", reserved, err)
	}
	reservedPort := *reserved.Options.Port
	startBlocker, startBlockerErr := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(savedSessionPortStart)))
	if startBlockerErr == nil {
		defer startBlocker.Close()
	} else if !errors.Is(startBlockerErr, syscall.EADDRINUSE) {
		t.Fatal(startBlockerErr)
	}
	fallbackPort := launch()
	if fallbackPort == occupiedPort || fallbackPort == reservedPort || fallbackPort <= reservedPort {
		t.Fatalf("fallback saved-session port = %d with occupied preference %d and reserved port %d; want a later unreserved port", fallbackPort, occupiedPort, reservedPort)
	}
	saved, err = store.Resolve("remember-port")
	if err != nil || saved.Options.Port == nil || *saved.Options.Port != fallbackPort {
		t.Fatalf("saved fallback port = %#v, %v; want %d", saved.Options.Port, err, fallbackPort)
	}
}

func TestPortAllocationStarts(t *testing.T) {
	if automaticPortStart != 60000 {
		t.Fatalf("direct target port start = %d, want 60000", automaticPortStart)
	}
	if savedSessionPortStart != 61000 {
		t.Fatalf("saved-session port start = %d, want 61000", savedSessionPortStart)
	}
}

func TestNormalizeFlagOrderPreservesSeparator(t *testing.T) {
	got := normalizeFlagOrder([]string{"-serve", "--", "-folder"})
	want := []string{"-serve", "--", "-folder"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("normalized arguments = %q, want %q", got, want)
	}
}

func TestHelpIsRecognized(t *testing.T) {
	if _, err := parseFlags([]string{"--help"}, io.Discard); !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("--help error = %v, want flag.ErrHelp", err)
	}
}

func TestVersion(t *testing.T) {
	for _, argument := range []string{"-version", "-v"} {
		var output bytes.Buffer
		if err := run([]string{argument}, &output); err != nil {
			t.Fatal(err)
		}
		if got, want := output.String(), "open-server "+version+"\n"; got != want {
			t.Errorf("%s output = %q, want %q", argument, got, want)
		}
	}
}

func TestListenLoopbackUsesIPv4Only(t *testing.T) {
	listener, err := listenLoopback(0)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok || !address.IP.Equal(net.IPv4(127, 0, 0, 1)) {
		t.Fatalf("listener address = %#v, want 127.0.0.1", listener.Addr())
	}
}

func TestListenLoopbackSkipsOccupiedPort(t *testing.T) {
	occupied, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	occupiedPort := occupied.Addr().(*net.TCPAddr).Port
	if occupiedPort == 65535 {
		t.Skip("ephemeral listener used the final TCP port")
	}
	listener, assignedPort, err := listenLoopbackFrom(occupiedPort)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if assignedPort <= occupiedPort {
		t.Fatalf("assigned port = %d, want greater than occupied port %d", assignedPort, occupiedPort)
	}
}

func TestLogicalRemoteRootPreservesSpecifiedSymlinkNamespace(t *testing.T) {
	backend := fixedRealPathBackend{workingDirectory: "/home/user"}
	absolute, err := logicalRemoteRoot(context.Background(), backend, "/data/logical-link/../root-link")
	if err != nil || absolute != "/data/root-link" {
		t.Fatalf("absolute logical root = %q, %v", absolute, err)
	}
	relative, err := logicalRemoteRoot(context.Background(), backend, "projects/../root-link")
	if err != nil || relative != "/home/user/root-link" {
		t.Fatalf("relative logical root = %q, %v", relative, err)
	}
	home, err := logicalRemoteRoot(context.Background(), backend, "~/projects/../root-link")
	if err != nil || home != "/home/user/root-link" {
		t.Fatalf("home-relative logical root = %q, %v", home, err)
	}
	tilde, err := logicalRemoteRoot(context.Background(), backend, "~")
	if err != nil || tilde != "/home/user" {
		t.Fatalf("home logical root = %q, %v", tilde, err)
	}
	userHome, err := logicalRemoteRoot(context.Background(), backend, "~other/projects")
	if err != nil || userHome != "/home/user/~other/projects" {
		t.Fatalf("other-user logical root = %q, %v", userHome, err)
	}
}

func TestRunDurationCleansUp(t *testing.T) {
	var output bytes.Buffer
	started := time.Now()
	err := run([]string{"--rsh", cliWrapper(t, false), "--duration", "500ms", "--no-open", "test-host:."}, &output)
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(started) > 4*time.Second {
		t.Fatal("duration-based cleanup took too long")
	}
	if !strings.Contains(output.String(), "duration expired") {
		t.Fatalf("session output = %q", output.String())
	}
}

func TestRunServeDurationCleansUp(t *testing.T) {
	root := t.TempDir()
	var output bytes.Buffer
	started := time.Now()
	err := run([]string{"-serve", "-address", "127.0.0.1", "-token", "test-token", "-duration", "100ms", root}, &output)
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(started) > 4*time.Second {
		t.Fatal("serve duration-based cleanup took too long")
	}
	for _, want := range []string{"Open this network URL: http://127.0.0.1:", "?token=test-token", "WARNING: serve mode uses plain, unencrypted HTTP", "duration expired"} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("serve output %q does not contain %q", output.String(), want)
		}
	}
}

func TestRunSignalCleansUp(t *testing.T) {
	output := newNotifyWriter("Open this local URL")
	result := make(chan error, 1)
	wrapper := cliWrapper(t, false)
	go func() { result <- run([]string{"--rsh", wrapper, "--no-open", "test-host:."}, output) }()
	select {
	case <-output.ready:
	case <-time.After(5 * time.Second):
		t.Fatalf("session did not start: %s", output.String())
	}
	process, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if err := process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("signal did not shut down the complete session")
	}
}

func TestRunReportsUnexpectedSSHExit(t *testing.T) {
	var output bytes.Buffer
	started := time.Now()
	err := run([]string{"--rsh", cliWrapper(t, true), "--no-open", "test-host:."}, &output)
	if err == nil || !strings.Contains(err.Error(), "SSH connection closed unexpectedly") {
		t.Fatalf("run error = %v, output = %q", err, output.String())
	}
	if time.Since(started) > 5*time.Second {
		t.Fatal("unexpected SSH exit was not handled promptly")
	}
}

func TestCLIHelperProcess(t *testing.T) {
	if os.Getenv("OPEN_SERVER_CLI_HELPER") != "1" {
		return
	}
	server, err := sftp.NewServer(struct {
		io.Reader
		io.WriteCloser
	}{os.Stdin, os.Stdout})
	if err != nil {
		os.Exit(2)
	}
	if os.Getenv("OPEN_SERVER_CLI_EXIT") == "1" {
		go func() { _ = server.Serve() }()
		time.Sleep(2 * time.Second)
		os.Exit(0)
	}
	_ = server.Serve()
	_ = server.Close()
	os.Exit(0)
}

func quoteShell(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func cliWrapper(t *testing.T, exitEarly bool) string {
	t.Helper()
	wrapper := filepath.Join(t.TempDir(), "ssh-wrapper")
	exitVariable := ""
	if exitEarly {
		exitVariable = " OPEN_SERVER_CLI_EXIT=1"
	}
	contents := "#!/bin/sh\nOPEN_SERVER_CLI_HELPER=1" + exitVariable + " " + quoteShell(os.Args[0]) + " -test.run='^TestCLIHelperProcess$' <&0 >&1 2>&2 &\nchild=$!\nexec >/dev/null 2>/dev/null\nwait \"$child\"\n"
	if err := os.WriteFile(wrapper, []byte(contents), 0o700); err != nil {
		t.Fatal(err)
	}
	return wrapper
}

type notifyWriter struct {
	mu        sync.Mutex
	buffer    bytes.Buffer
	needle    string
	ready     chan struct{}
	readyOnce sync.Once
}

type fixedRealPathBackend struct {
	filesystem.Backend
	workingDirectory string
}

func (b fixedRealPathBackend) RealPath(context.Context, string) (string, error) {
	return b.workingDirectory, nil
}

func newNotifyWriter(needle string) *notifyWriter {
	return &notifyWriter{needle: needle, ready: make(chan struct{})}
}

func (w *notifyWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.buffer.Write(p)
	if strings.Contains(w.buffer.String(), w.needle) {
		w.readyOnce.Do(func() { close(w.ready) })
	}
	return n, err
}

func (w *notifyWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buffer.String()
}
