//go:build !windows

package tensorboard

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// fakeServiceHelper stands in for the TensorBoard helper in script-lifecycle
// tests. It records its own PID (which equals its process-group ID under
// setsid) so the test can watch the service group, then sleeps indefinitely.
const fakeServiceHelper = `import os, time
directory = os.path.dirname(os.path.abspath(__file__))
with open(os.path.join(directory, "service.pid"), "w") as handle:
    handle.write(str(os.getpid()))
time.sleep(3600)
`

func requirePython3(t *testing.T) string {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable")
	}
	return python
}

// startAttachScript runs the generated attach script locally with a fake
// service, feeds it a valid launch frame, and returns the running shell plus
// the SSH-stdin write end so the test can drive SHUTDOWN or an unclean EOF.
func startAttachScript(t *testing.T, python, runtimeDirectory, remoteSocket, checkSeconds string, idleLimit int) (*exec.Cmd, *os.File) {
	t.Helper()
	script := remoteTensorBoardScript(python, runtimeDirectory, remoteSocket, fakeServiceHelper, checkSeconds, idleLimit)
	command := exec.Command("sh", "-c", script)
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	command.Stdin = reader
	// Real *os.File stdio keeps os/exec from spawning copier goroutines, so
	// command.Wait returns when the shell exits rather than waiting on the
	// detached service and keeper that inherit these descriptors.
	devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	command.Stdout = devnull
	command.Stderr = devnull
	configureCommand(command)
	if err := command.Start(); err != nil {
		_ = reader.Close()
		_ = writer.Close()
		_ = devnull.Close()
		t.Fatal(err)
	}
	_ = reader.Close()
	_ = devnull.Close()
	t.Cleanup(func() { _ = writer.Close() })

	frame, err := remoteConfigurationFrame(remoteConfiguration{
		Version:      1,
		LogDirectory: "/data/run",
		PathPrefix:   "/tensorboard/abc",
		SocketPath:   remoteSocket,
		Token:        strings.Repeat("a", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(frame); err != nil {
		t.Fatal(err)
	}
	return command, writer
}

func waitServicePID(t *testing.T, runtimeDirectory string) int {
	t.Helper()
	pidPath := filepath.Join(runtimeDirectory, "service.pid")
	deadline := time.Now().Add(10 * time.Second)
	for {
		if data, err := os.ReadFile(pidPath); err == nil {
			if pid, convErr := strconv.Atoi(strings.TrimSpace(string(data))); convErr == nil && pid > 0 {
				return pid
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("fake TensorBoard service did not start")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func processGroupAlive(pid int) bool {
	return syscall.Kill(-pid, 0) == nil
}

// waitWatcherPID blocks until watcher.pid holds a positive integer and returns
// it, so tests can confirm which shell currently owns the lease.
func waitWatcherPID(t *testing.T, watcherPath string, within time.Duration) int {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		if data, err := os.ReadFile(watcherPath); err == nil {
			if pid, convErr := strconv.Atoi(strings.TrimSpace(string(data))); convErr == nil && pid > 0 {
				return pid
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("watcher.pid was not written within %s", within)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func waitProcessExit(t *testing.T, command *exec.Cmd) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("attach shell did not exit")
	}
}

func waitGroupGone(pid int, within time.Duration) bool {
	deadline := time.Now().Add(within)
	for {
		if syscall.Kill(-pid, 0) != nil {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func waitPathGone(path string, within time.Duration) bool {
	deadline := time.Now().Add(within)
	for {
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// leaseIsFree reports whether a non-attached shell can take the lease, i.e. no
// live attach shell is holding it.
func leaseIsFree(leasePath string) bool {
	probe := exec.Command("sh", "-c", `( flock -w 2 8 ) 8>"$1"`, "lease-probe", leasePath)
	return probe.Run() == nil
}

func TestRemoteScriptSurvivesUncleanDisconnect(t *testing.T) {
	python := requirePython3(t)
	runtimeDirectory := filepath.Join(t.TempDir(), "runtime")
	remoteSocket := filepath.Join(runtimeDirectory, "tensorboard.sock")
	// A large idle limit keeps the grace window from expiring during the test.
	command, writer := startAttachScript(t, python, runtimeDirectory, remoteSocket, "0.1", 1000)
	pid := waitServicePID(t, runtimeDirectory)
	defer syscall.Kill(-pid, syscall.SIGKILL)

	// Simulate an unclean disconnect: the SSH stdin reaches EOF, so the attach
	// shell exits and its lease releases without any shutdown marker.
	_ = writer.Close()
	waitProcessExit(t, command)

	if !leaseIsFree(filepath.Join(runtimeDirectory, "lease")) {
		t.Fatal("lease was not released after unclean disconnect")
	}
	// The keeper must preserve the service across many poll intervals.
	time.Sleep(1500 * time.Millisecond)
	if !processGroupAlive(pid) {
		t.Fatal("service did not survive unclean disconnect")
	}
	if _, err := os.Stat(runtimeDirectory); err != nil {
		t.Fatalf("runtime directory removed while service should survive: %v", err)
	}
}

func TestRemoteScriptShutdownMarkerStopsService(t *testing.T) {
	python := requirePython3(t)
	runtimeDirectory := filepath.Join(t.TempDir(), "runtime")
	remoteSocket := filepath.Join(runtimeDirectory, "tensorboard.sock")
	command, writer := startAttachScript(t, python, runtimeDirectory, remoteSocket, "0.1", 1000)
	pid := waitServicePID(t, runtimeDirectory)
	defer syscall.Kill(-pid, syscall.SIGKILL)

	// Explicit close: send SHUTDOWN, then let the SSH stdin reach EOF.
	if _, err := writer.Write([]byte("SHUTDOWN\n")); err != nil {
		t.Fatal(err)
	}
	_ = writer.Close()
	waitProcessExit(t, command)

	if !waitGroupGone(pid, 5*time.Second) {
		t.Fatal("service was not stopped promptly after SHUTDOWN")
	}
	if !waitPathGone(runtimeDirectory, 5*time.Second) {
		t.Fatal("runtime directory was not removed after SHUTDOWN")
	}
}

func TestRemoteScriptGraceExpiryReclaimsService(t *testing.T) {
	python := requirePython3(t)
	runtimeDirectory := filepath.Join(t.TempDir(), "runtime")
	remoteSocket := filepath.Join(runtimeDirectory, "tensorboard.sock")
	// A tiny grace window (2 idle checks of 0.1s) expires soon after the drop.
	command, writer := startAttachScript(t, python, runtimeDirectory, remoteSocket, "0.1", 2)
	pid := waitServicePID(t, runtimeDirectory)
	defer syscall.Kill(-pid, syscall.SIGKILL)

	// Unclean disconnect with no marker: the grace timer must reclaim it.
	_ = writer.Close()
	waitProcessExit(t, command)

	if !waitGroupGone(pid, 5*time.Second) {
		t.Fatal("service was not reclaimed after the grace window")
	}
	if !waitPathGone(runtimeDirectory, 5*time.Second) {
		t.Fatal("runtime directory was not removed after the grace window")
	}
}

func TestRemoteReattachScriptHoldsLeaseAndRequiresSocket(t *testing.T) {
	runtimeDirectory := t.TempDir()
	remoteSocket := filepath.Join(runtimeDirectory, "tensorboard.sock")

	// Without a live service socket, re-attach must fail.
	missing := exec.Command("sh", "-c", remoteReattachScript(runtimeDirectory, remoteSocket))
	missing.Stdin = strings.NewReader("")
	if err := missing.Run(); err == nil {
		t.Fatal("re-attach succeeded without a service socket")
	}

	// With the socket present, re-attach announces readiness and holds the lease.
	listener, err := net.Listen("unix", remoteSocket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	command := exec.Command("sh", "-c", remoteReattachScript(runtimeDirectory, remoteSocket))
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	command.Stdin = reader
	diagnostic := newCaptureWriter(nil, "")
	readiness := newReadinessWriter(diagnostic, remoteReadySentinel)
	command.Stdout = readiness
	command.Stderr = diagnostic
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	_ = reader.Close()
	finished := false
	defer func() {
		if !finished {
			_ = writer.Close()
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	}()

	select {
	case <-readiness.Ready():
	case <-time.After(10 * time.Second):
		t.Fatalf("re-attach did not report readiness: %s", diagnostic.String())
	}

	// While re-attach holds the lease, a non-blocking outside probe must fail.
	if leaseIsHeld := exec.Command("sh", "-c", `( flock -n 8 ) 8>"$1"`, "lease-probe", filepath.Join(runtimeDirectory, "lease")).Run(); leaseIsHeld == nil {
		t.Fatal("re-attach did not hold the lease")
	}

	if _, err := writer.Write([]byte("SHUTDOWN\n")); err != nil {
		t.Fatal(err)
	}
	_ = writer.Close()
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case err := <-done:
		readiness.Flush()
		diagnostic.Flush()
		if err != nil {
			t.Fatalf("re-attach exit: %v; output %s", err, diagnostic.String())
		}
		finished = true
	case <-time.After(5 * time.Second):
		t.Fatalf("re-attach did not exit after SHUTDOWN: %s", diagnostic.String())
	}
	if _, err := os.Stat(filepath.Join(runtimeDirectory, "shutdown")); err != nil {
		t.Fatalf("SHUTDOWN marker was not created: %v", err)
	}
}

func TestRemoteReattachScriptEvictsStaleLeaseHolder(t *testing.T) {
	runtimeDirectory := t.TempDir()
	remoteSocket := filepath.Join(runtimeDirectory, "tensorboard.sock")
	leasePath := filepath.Join(runtimeDirectory, "lease")
	watcherPath := filepath.Join(runtimeDirectory, "watcher.pid")

	// A live service socket must exist for re-attach to proceed.
	listener, err := net.Listen("unix", remoteSocket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	// A stale holder mimics the old attach shell whose dead TCP peer sshd has not
	// yet noticed: it takes the exclusive lease, records its PID, then execs into
	// a long sleep so the same PID keeps holding the lock with no forked child
	// inheriting fd 9. Only a signal releases it.
	staleScript := strings.Join([]string{
		`exec 9>"$1/lease" || exit 1`,
		`flock 9 || exit 1`,
		`printf '%s\n' "$$" > "$1/watcher.pid"`,
		`exec sleep 3600`,
	}, "\n")
	stale := exec.Command("sh", "-c", staleScript, "stale-holder", runtimeDirectory)
	configureCommand(stale)
	if err := stale.Start(); err != nil {
		t.Fatal(err)
	}
	stalePID := stale.Process.Pid
	staleExited := make(chan error, 1)
	go func() { staleExited <- stale.Wait() }()
	t.Cleanup(func() { _ = syscall.Kill(-stalePID, syscall.SIGKILL) })

	// The holder writes watcher.pid only after taking the lease, so its presence
	// proves the lease is held.
	if got := waitWatcherPID(t, watcherPath, 5*time.Second); got != stalePID {
		t.Fatalf("stale holder recorded pid %d, want %d", got, stalePID)
	}
	if leaseIsFree(leasePath) {
		t.Fatal("stale holder did not actually hold the lease")
	}

	// Run re-attach; it must evict the stale holder and win the lease.
	command := exec.Command("sh", "-c", remoteReattachScript(runtimeDirectory, remoteSocket))
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	command.Stdin = reader
	diagnostic := newCaptureWriter(nil, "")
	readiness := newReadinessWriter(diagnostic, remoteReadySentinel)
	command.Stdout = readiness
	command.Stderr = diagnostic
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	_ = reader.Close()
	finished := false
	t.Cleanup(func() {
		if !finished {
			_ = writer.Close()
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	})

	// (a) Re-attach announces readiness only after acquiring the lease.
	select {
	case <-readiness.Ready():
	case <-time.After(10 * time.Second):
		t.Fatalf("re-attach did not acquire the lease: %s", diagnostic.String())
	}

	// (b) The stale holder must have been killed during eviction.
	select {
	case <-staleExited:
	case <-time.After(5 * time.Second):
		t.Fatalf("stale holder was not killed by re-attach: %s", diagnostic.String())
	}

	// (c) watcher.pid now records the re-attach shell rather than the old holder.
	newPID := waitWatcherPID(t, watcherPath, 5*time.Second)
	if newPID == stalePID {
		t.Fatalf("watcher.pid still records the stale holder %d", stalePID)
	}
	if newPID != command.Process.Pid {
		t.Fatalf("watcher.pid = %d, want re-attach pid %d", newPID, command.Process.Pid)
	}

	// Clean shutdown of the re-attach shell.
	if _, err := writer.Write([]byte("SHUTDOWN\n")); err != nil {
		t.Fatal(err)
	}
	_ = writer.Close()
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("re-attach exit: %v; output %s", err, diagnostic.String())
		}
		finished = true
	case <-time.After(5 * time.Second):
		t.Fatalf("re-attach did not exit after SHUTDOWN: %s", diagnostic.String())
	}
}

func TestRemoteHelperServesAuthenticatedUnixSocketAndCleansUp(t *testing.T) {
	python := requirePython3(t)
	runtimeDirectory := t.TempDir()
	if err := os.Chmod(runtimeDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	helperPath := filepath.Join(runtimeDirectory, "helper.py")
	if err := os.WriteFile(helperPath, []byte(remoteTensorBoardHelper), 0o600); err != nil {
		t.Fatal(err)
	}
	fakeRoot := t.TempDir()
	packageDirectory := filepath.Join(fakeRoot, "tensorboard")
	if err := os.Mkdir(packageDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(packageDirectory, "__init__.py"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	fakeProgram := `import threading


class _Flags:
    path_prefix = ""
    load_fast = "auto"
    grpc_data_provider = "localhost:1234"


class TensorBoard:
    def __init__(self, server_class=None):
        self.server_class = server_class
        self.flags = _Flags()

    def configure(self, argv):
        self.flags.path_prefix = argv[argv.index("--path_prefix") + 1]

    def launch(self):
        if self.flags.load_fast != "false":
            raise RuntimeError("fast loader was not disabled")
        if self.flags.grpc_data_provider:
            raise RuntimeError("gRPC provider was not disabled")

        def application(environ, start_response):
            if environ.get("HTTP_AUTHORIZATION"):
                body = b"authorization leaked"
                start_response("500 Internal Server Error", [("Content-Length", str(len(body)))])
                return [body]
            body = b"ok"
            start_response("200 OK", [("Content-Length", str(len(body)))])
            return [body]

        self.server = self.server_class(application, self.flags)
        thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        thread.start()
        return self.server.get_url()
`
	if err := os.WriteFile(filepath.Join(packageDirectory, "program.py"), []byte(fakeProgram), 0o600); err != nil {
		t.Fatal(err)
	}

	socketPath := filepath.Join(runtimeDirectory, "tensorboard.sock")
	token := strings.Repeat("a", 64)
	frame, err := remoteConfigurationFrame(remoteConfiguration{
		Version:      1,
		LogDirectory: "/data/private run",
		PathPrefix:   "/tensorboard/abc",
		SocketPath:   socketPath,
		Token:        token,
	})
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(python, "-B", helperPath)
	command.Env = environmentWith("PYTHONPATH", fakeRoot)
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	diagnostic := newCaptureWriter(nil, token)
	readiness := newReadinessWriter(diagnostic, remoteReadySentinel)
	command.Stdout = readiness
	command.Stderr = diagnostic
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	waitErr := make(chan error, 1)
	go func() { waitErr <- command.Wait() }()
	finished := false
	defer func() {
		if !finished {
			_ = command.Process.Kill()
			select {
			case <-waitErr:
			case <-time.After(2 * time.Second):
			}
		}
	}()
	if _, err := stdin.Write(frame); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(command.Args, " "), token) || strings.Contains(strings.Join(command.Args, " "), "/data/private run") {
		t.Fatalf("private configuration leaked into argv: %q", command.Args)
	}
	select {
	case <-readiness.Ready():
	case <-time.After(10 * time.Second):
		t.Fatalf("helper did not report readiness: %s", diagnostic.String())
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		if info, statErr := os.Stat(socketPath); statErr == nil {
			if got := info.Mode().Perm(); got != 0o600 {
				t.Fatalf("socket mode = %04o, want 0600", got)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("helper did not create socket: %s", diagnostic.String())
		}
		time.Sleep(20 * time.Millisecond)
	}

	unauthorized := unixHTTPRequest(t, socketPath, "")
	if !strings.HasPrefix(unauthorized, "HTTP/1.0 401") {
		t.Fatalf("unauthorized response = %q", unauthorized)
	}
	authorized := unixHTTPRequest(t, socketPath, "Authorization: Bearer "+token+"\r\n")
	if !strings.HasPrefix(authorized, "HTTP/1.0 200") || !strings.HasSuffix(authorized, "ok") {
		t.Fatalf("authorized response = %q", authorized)
	}

	// Stdin EOF must NOT stop the service; only a termination signal may.
	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)
	if err := command.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("helper exited on stdin EOF (should survive): %v; output %s", err, diagnostic.String())
	}

	if err := command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-waitErr:
		readiness.Flush()
		diagnostic.Flush()
		if err != nil {
			t.Fatalf("helper exit after SIGTERM: %v; output %s", err, diagnostic.String())
		}
		finished = true
	case <-time.After(5 * time.Second):
		t.Fatalf("helper did not stop after SIGTERM: %s", diagnostic.String())
	}
	if _, err := os.Stat(socketPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("helper socket survived shutdown: %v", err)
	}
}

func environmentWith(name, value string) []string {
	prefix := name + "="
	environment := make([]string, 0, len(os.Environ())+1)
	for _, item := range os.Environ() {
		if !strings.HasPrefix(item, prefix) {
			environment = append(environment, item)
		}
	}
	return append(environment, prefix+value)
}

func unixHTTPRequest(t *testing.T, socketPath, extraHeader string) string {
	t.Helper()
	connection, err := net.DialTimeout("unix", socketPath, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if _, err := fmt.Fprintf(connection, "GET /tensorboard/abc/ HTTP/1.0\r\nHost: localhost\r\n%s\r\n", extraHeader); err != nil {
		t.Fatal(err)
	}
	response, err := io.ReadAll(connection)
	if err != nil {
		t.Fatal(err)
	}
	return string(response)
}

func TestLocalTensorBoardStopTerminatesProcessGroup(t *testing.T) {
	command := exec.Command("sh", "-c", "sleep 30 & wait")
	configureCommand(command)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	processGroup := command.Process.Pid
	process := newRunningProcess(command, nil)
	assertProcessRunning(t, process)
	process.stop()

	deadline := time.Now().Add(time.Second)
	for {
		err := syscall.Kill(-processGroup, 0)
		if errors.Is(err, syscall.ESRCH) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("TensorBoard process group %d survived shutdown: %v", processGroup, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func assertProcessRunning(t *testing.T, process *runningProcess) {
	t.Helper()
	select {
	case <-process.done:
		t.Fatal("test process exited before shutdown")
	case <-time.After(100 * time.Millisecond):
	}
}
