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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pkg/sftp"

	"open-server/internal/filesystem"
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
	if configuration.port != 8123 || configuration.duration != 90*time.Minute || configuration.title != "Files" || configuration.target != "lab:/tmp" || !configuration.tensorBoard || configuration.python != "/env/bin/python" || !configuration.latex {
		t.Fatalf("configuration = %#v", configuration)
	}
	add, err := parseFlags([]string{"--add", "work", "lab:/srv/work"}, io.Discard)
	if err != nil || add.addName != "work" || add.target != "lab:/srv/work" {
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
	serve, err := parseFlags([]string{"-serve"}, io.Discard)
	if err != nil || !serve.serve || serve.target != "." {
		t.Fatalf("serve configuration = %#v, %v", serve, err)
	}
	for _, arguments := range [][]string{
		{}, {"--port", "70000", "lab:/tmp"}, {"--duration", "-1s", "lab:/tmp"},
		{"lab:/one", "lab:/two"}, {"--add", "work"}, {"--list", "lab:/tmp"},
		{"--delete", "work", "lab:/tmp"}, {"--add", "work", "--list", "lab:/tmp"},
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
	if !strings.Contains(addOutput.String(), "-tensorboard=true") || !strings.Contains(addOutput.String(), `-python-interpreter="/env/bin/python"`) || !strings.Contains(addOutput.String(), "-latex=true") {
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
	if options.TensorBoard == nil || !*options.TensorBoard || options.Python == nil || *options.Python != "/env/bin/python" || options.LaTeX == nil || !*options.LaTeX || options.Title == nil || *options.Title != "Dashboards" {
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
