//go:build !windows

package jupyter

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestProcessLauncherStartsAuthenticatesAndCleansUp(t *testing.T) {
	temporaryDirectory := t.TempDir()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	serverWrapper := filepath.Join(temporaryDirectory, "jupyter-lab")
	wrapper := "#!/bin/sh\nexec \"$JUPYTER_TEST_BINARY\" -test.run=TestJupyterHelperProcess -- \"$@\"\n"
	if err := os.WriteFile(serverWrapper, []byte(wrapper), 0o755); err != nil {
		t.Fatal(err)
	}
	kernelPython := filepath.Join(temporaryDirectory, "kernel python")
	kernelInstaller := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$JUPYTER_TEST_KERNEL_ARGS\"\n"
	if err := os.WriteFile(kernelPython, []byte(kernelInstaller), 0o755); err != nil {
		t.Fatal(err)
	}
	argumentFile := filepath.Join(temporaryDirectory, "server-arguments")
	kernelArgumentFile := filepath.Join(temporaryDirectory, "kernel-arguments")
	environmentFile := filepath.Join(temporaryDirectory, "server-environment")
	deleteFile := filepath.Join(temporaryDirectory, "kernel-deleted")
	t.Setenv("GO_WANT_JUPYTER_HELPER_PROCESS", "1")
	t.Setenv("JUPYTER_TEST_BINARY", binary)
	t.Setenv("JUPYTER_TEST_ARGS", argumentFile)
	t.Setenv("JUPYTER_TEST_KERNEL_ARGS", kernelArgumentFile)
	t.Setenv("JUPYTER_TEST_ENV", environmentFile)
	t.Setenv("JUPYTER_TEST_DELETE", deleteFile)
	t.Setenv("PATH", temporaryDirectory+string(os.PathListSeparator)+os.Getenv("PATH"))

	var output strings.Builder
	launcher := NewLocal("", &output)
	instance, err := launcher.Start("/data/run one", kernelPython, "/jupyter/abc")
	if err != nil {
		t.Fatal(err)
	}
	if instance.Target == nil || len(instance.Token) != 64 {
		t.Fatalf("instance = %#v", instance)
	}
	if !instance.Available() || instance.Done() == nil {
		t.Fatalf("new instance lifecycle = available %v, done %#v", instance.Available(), instance.Done())
	}
	if err := launcher.Stop(instance); err != nil {
		t.Fatal(err)
	}
	if instance.Available() {
		t.Fatal("stopped instance is still available")
	}
	select {
	case <-instance.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("stopped instance did not complete")
	}
	if len(launcher.instances) != 0 {
		t.Fatalf("stopped instance remains registered: %#v", launcher.instances)
	}
	if err := launcher.Stop(instance); err != nil {
		t.Fatalf("second Stop: %v", err)
	}
	launcher.Close()
	launcher.Close()

	serverArguments, err := os.ReadFile(argumentFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(serverArguments), instance.Token) {
		t.Fatalf("Jupyter token leaked into arguments: %s", serverArguments)
	}
	if !strings.Contains(string(serverArguments), "--MappingKernelManager.default_kernel_name=open-server-") {
		t.Fatalf("temporary kernel was not made default: %s", serverArguments)
	}
	if _, err := os.Stat(deleteFile); err != nil {
		t.Fatalf("kernel DELETE was not received: %v", err)
	}
	if strings.Contains(output.String(), instance.Token) || !strings.Contains(output.String(), "[redacted]") {
		t.Fatalf("server diagnostics did not redact the token: %q", output.String())
	}
	kernelArguments, err := os.ReadFile(kernelArgumentFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"-m", "ipykernel", "install", "--prefix", "--name", "--display-name", "Python (open-server)"} {
		if !strings.Contains(string(kernelArguments), want) {
			t.Errorf("kernel arguments %q do not contain %q", kernelArguments, want)
		}
	}
	environment, err := os.ReadFile(environmentFile)
	if err != nil {
		t.Fatal(err)
	}
	kernelPrefix := environmentValue(string(environment), "JUPYTER_PATH")
	if !strings.HasSuffix(kernelPrefix, filepath.Join("share", "jupyter")) {
		t.Fatalf("JUPYTER_PATH = %q", kernelPrefix)
	}
	prefix := strings.TrimSuffix(kernelPrefix, filepath.Join("share", "jupyter"))
	runtimeDirectory := filepath.Dir(filepath.Clean(prefix))
	pythonPath := strings.Split(environmentValue(string(environment), "PYTHONPATH"), string(os.PathListSeparator))[0]
	if pythonPath != runtimeDirectory {
		t.Fatalf("temporary module PYTHONPATH = %q, want %q", pythonPath, runtimeDirectory)
	}
	if _, err := os.Stat(runtimeDirectory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary Jupyter runtime survived Close: %v", err)
	}
}

func TestJupyterHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_JUPYTER_HELPER_PROCESS") != "1" {
		return
	}
	arguments := helperArguments(os.Args)
	port := argumentValue(arguments, "--ServerApp.port=")
	baseURL := argumentValue(arguments, "--ServerApp.base_url=")
	if port == "" || baseURL == "" {
		os.Exit(2)
	}
	if err := os.WriteFile(os.Getenv("JUPYTER_TEST_ARGS"), []byte(strings.Join(arguments, "\n")), 0o600); err != nil {
		os.Exit(3)
	}
	environment := "JUPYTER_PATH=" + os.Getenv("JUPYTER_PATH") + "\n" +
		"PYTHONPATH=" + os.Getenv("PYTHONPATH") + "\n"
	if err := os.WriteFile(os.Getenv("JUPYTER_TEST_ENV"), []byte(environment), 0o600); err != nil {
		os.Exit(4)
	}
	moduleDirectory := strings.Split(os.Getenv("PYTHONPATH"), string(os.PathListSeparator))[0]
	if _, err := os.Stat(filepath.Join(moduleDirectory, contentsManagerModule+".py")); err != nil {
		os.Exit(7)
	}
	token := os.Getenv("JUPYTER_TOKEN")
	_, _ = fmt.Fprintf(os.Stderr, "server token=%s\n", token)
	kernelRunning := true
	mux := http.NewServeMux()
	mux.HandleFunc(baseURL+"api/status", func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "token "+token {
			http.Error(writer, "unauthorized", http.StatusUnauthorized)
			return
		}
		_, _ = writer.Write([]byte(`{}`))
	})
	mux.HandleFunc(baseURL+"api/kernels", func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "token "+token {
			http.Error(writer, "unauthorized", http.StatusUnauthorized)
			return
		}
		if kernelRunning {
			_, _ = writer.Write([]byte(`[{"id":"kernel-1"}]`))
		} else {
			_, _ = writer.Write([]byte(`[]`))
		}
	})
	mux.HandleFunc(baseURL+"api/kernels/kernel-1", func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodDelete || request.Header.Get("Authorization") != "token "+token {
			http.Error(writer, "unauthorized", http.StatusUnauthorized)
			return
		}
		kernelRunning = false
		if err := os.WriteFile(os.Getenv("JUPYTER_TEST_DELETE"), []byte("deleted\n"), 0o600); err != nil {
			os.Exit(5)
		}
		writer.WriteHeader(http.StatusNoContent)
	})
	if err := http.ListenAndServe("127.0.0.1:"+port, mux); err != nil {
		os.Exit(6)
	}
	os.Exit(0)
}

func helperArguments(arguments []string) []string {
	for index, argument := range arguments {
		if argument == "--" {
			return arguments[index+1:]
		}
	}
	return nil
}

func argumentValue(arguments []string, prefix string) string {
	for _, argument := range arguments {
		if strings.HasPrefix(argument, prefix) {
			return strings.TrimPrefix(argument, prefix)
		}
	}
	return ""
}

func environmentValue(environment, name string) string {
	prefix := name + "="
	for _, line := range strings.Split(environment, "\n") {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimPrefix(line, prefix)
		}
	}
	return ""
}

func TestWorkingDirectoryContentsManagerMovesFilesToPrivateTrash(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not installed")
	}
	moduleDirectory := t.TempDir()
	if err := writeContentsManagerModule(moduleDirectory); err != nil {
		t.Fatal(err)
	}
	modulePath := filepath.Join(moduleDirectory, contentsManagerModule+".py")
	moduleInfo, err := os.Stat(modulePath)
	if err != nil {
		t.Fatal(err)
	}
	if mode := moduleInfo.Mode().Perm(); mode != 0o600 {
		t.Fatalf("contents manager mode = %04o, want 0600", mode)
	}

	const probe = `
import asyncio
import importlib.util
import os
import stat
import sys
import types


def stub(name):
    module = types.ModuleType(name)
    sys.modules[name] = module
    return module


anyio = stub("anyio")
to_thread = stub("anyio.to_thread")


async def run_sync(function, *arguments):
    return function(*arguments)


to_thread.run_sync = run_sync
anyio.to_thread = to_thread
jupyter_core = stub("jupyter_core")
jupyter_paths = stub("jupyter_core.paths")
jupyter_paths.is_hidden = lambda path, root: False
jupyter_core.paths = jupyter_paths
jupyter_server = stub("jupyter_server")
services = stub("jupyter_server.services")
contents = stub("jupyter_server.services.contents")
largefilemanager = stub("jupyter_server.services.contents.largefilemanager")


class AsyncLargeFileManager:
    async def delete_file(self, path):
        self.delegated_delete = path


largefilemanager.AsyncLargeFileManager = AsyncLargeFileManager
jupyter_server.services = services
services.contents = contents
contents.largefilemanager = largefilemanager
tornado = stub("tornado")
web = stub("tornado.web")


class HTTPError(Exception):
    def __init__(self, status_code, message=None):
        super().__init__(message)
        self.status_code = status_code


web.HTTPError = HTTPError
tornado.web = web
spec = importlib.util.spec_from_file_location("open_server_jupyter", sys.argv[1])
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class Log:
    def debug(self, *arguments):
        pass


root = sys.argv[2]
manager = module.WorkingDirectoryContentsManager()
manager.root_dir = root
manager.delete_to_trash = True
manager.allow_hidden = False
manager.log = Log()
manager._get_os_path = lambda path: os.path.join(root, path)
manager.is_writable = lambda path: True
source = os.path.join(root, "report.txt")
for contents_value in ("first", "second"):
    with open(source, "w", encoding="utf-8") as handle:
        handle.write(contents_value)
    asyncio.run(manager.delete_file("report.txt"))

user_id = getattr(os, "getuid", lambda: 0)()
trash = os.path.join(root, f".Trash-{user_id}")
files = os.path.join(trash, "files")
info = os.path.join(trash, "info")
assert sorted(os.listdir(files)) == ["report 1.txt", "report.txt"]
assert sorted(os.listdir(info)) == ["report 1.txt.trashinfo", "report.txt.trashinfo"]
assert open(os.path.join(files, "report.txt"), encoding="utf-8").read() == "first"
assert open(os.path.join(files, "report 1.txt"), encoding="utf-8").read() == "second"
metadata = open(os.path.join(info, "report.txt.trashinfo"), encoding="utf-8").read()
assert metadata.startswith("[Trash Info]\nPath=")
assert "report.txt\nDeletionDate=" in metadata
if os.name != "nt":
    for directory in (trash, files, info):
        assert stat.S_IMODE(os.stat(directory).st_mode) == 0o700

manager.delete_to_trash = False
asyncio.run(manager.delete_file("delegated"))
assert manager.delegated_delete == "delegated"
`
	command := exec.Command(python, "-c", probe, modulePath, t.TempDir())
	if output, commandErr := command.CombinedOutput(); commandErr != nil {
		t.Fatalf("contents manager probe failed: %v\n%s", commandErr, output)
	}
}

func TestProcessLauncherUsesWorkingDirectoryTrashWithRealJupyter(t *testing.T) {
	python := os.Getenv("OPEN_SERVER_TEST_JUPYTER_PYTHON")
	if python == "" {
		t.Skip("OPEN_SERVER_TEST_JUPYTER_PYTHON is not set")
	}
	probe := exec.Command(
		python,
		"-c",
		"import jupyterlab, jupyter_server.services.contents.largefilemanager",
	)
	if output, err := probe.CombinedOutput(); err != nil {
		t.Fatalf("configured Jupyter Python is unavailable: %v\n%s", err, output)
	}

	workingDirectory := t.TempDir()
	deletedPath := filepath.Join(workingDirectory, "delete-me.txt")
	if err := os.WriteFile(deletedPath, []byte("recoverable\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	launcher := NewLocal(python, &output)
	defer launcher.Close()
	instance, err := launcher.Start(workingDirectory, "", "/jupyter/real-trash")
	if err != nil {
		t.Fatalf("start real JupyterLab: %v\n%s", err, output.String())
	}
	request, err := http.NewRequest(
		http.MethodDelete,
		instance.Target.String()+"/jupyter/real-trash/api/contents/delete-me.txt",
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "token "+instance.Token)
	response, err := (&http.Client{Timeout: 10 * time.Second}).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		t.Fatalf("delete status = %s: %s\n%s", response.Status, body, output.String())
	}

	userID := os.Getuid()
	trashedPath := filepath.Join(
		workingDirectory, fmt.Sprintf(".Trash-%d", userID), "files", "delete-me.txt",
	)
	contents, err := os.ReadFile(trashedPath)
	if err != nil {
		t.Fatalf("read working-directory trash: %v\n%s", err, output.String())
	}
	if string(contents) != "recoverable\n" {
		t.Fatalf("trashed contents = %q", contents)
	}
}

// requireProcessTools skips a test when the shell utilities the remote scripts
// depend on are unavailable on the host running the unit tests.
func requireProcessTools(t *testing.T) {
	t.Helper()
	for _, tool := range []string{"flock", "setsid"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed", tool)
		}
	}
}

// makeRemoteRunDir creates a private run directory whose path matches the
// /tmp/osj-* guard so the keeper is allowed to remove it.
func makeRemoteRunDir(t *testing.T) string {
	t.Helper()
	runDir, _, err := newRemoteRuntime()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(runDir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runDir) })
	return runDir
}

// startSupervisedFakeService runs the supervised remote script locally with a
// sleep-based fake service that records its own process-group id. The returned
// process wraps the attached shell; writer is its SHUTDOWN control pipe.
func startSupervisedFakeService(
	t *testing.T, runDir string, grace, check time.Duration,
) (*runningProcess, *os.File, int) {
	t.Helper()
	pidFile := filepath.Join(runDir, "service.pid")
	innerService := `echo $$ > ` + shellQuote(pidFile) + `; exec sleep 300`
	serviceCommand := `sh -c ` + shellQuote(innerService)
	script := supervisedRemoteScript([]string{"runtime_created=1"}, serviceCommand, runDir, grace, check)
	command := exec.Command("sh", "-c", script)
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	command.Stdin = reader
	configureCommand(command)
	if err := command.Start(); err != nil {
		_ = reader.Close()
		_ = writer.Close()
		t.Fatal(err)
	}
	_ = reader.Close()
	process := newRunningProcess(command, writer, nil)
	pgid := waitForServicePGID(t, pidFile)
	t.Cleanup(func() {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		_ = writer.Close()
		select {
		case <-process.done:
		case <-time.After(2 * time.Second):
		}
	})
	return process, writer, pgid
}

func waitForServicePGID(t *testing.T, pidFile string) int {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(pidFile); err == nil {
			if pid, convErr := strconv.Atoi(strings.TrimSpace(string(data))); convErr == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("fake service did not report its process group")
	return 0
}

func processGroupAlive(pgid int) bool {
	return syscall.Kill(-pgid, 0) == nil
}

func leaseHeld(t *testing.T, leasePath string) bool {
	t.Helper()
	file, err := os.OpenFile(leasePath, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open lease: %v", err)
	}
	defer file.Close()
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		return false
	}
	return true
}

func waitForServiceStopped(t *testing.T, pgid int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !processGroupAlive(pgid) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("service process group %d survived", pgid)
}

func waitForRunDirRemoved(t *testing.T, runDir string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(runDir); errors.Is(err, os.ErrNotExist) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("run directory %s survived", runDir)
}

// TestRemoteScriptSurvivesUncleanDisconnect covers the core reconnect goal: an
// EOF with no SHUTDOWN (a dropped tunnel) must leave the service running and
// the run directory intact for the grace window.
func TestRemoteScriptSurvivesUncleanDisconnect(t *testing.T) {
	requireProcessTools(t)
	runDir := makeRemoteRunDir(t)
	process, writer, pgid := startSupervisedFakeService(t, runDir, 30*time.Second, 200*time.Millisecond)

	if err := writer.Close(); err != nil { // unclean drop: EOF, no SHUTDOWN
		t.Fatal(err)
	}
	select {
	case <-process.done:
	case <-time.After(2 * time.Second):
		t.Fatal("attached shell did not exit on stdin EOF")
	}

	time.Sleep(800 * time.Millisecond) // several keeper probes at 200ms
	if !processGroupAlive(pgid) {
		t.Fatal("service was killed after an unclean disconnect within the grace window")
	}
	if _, err := os.Stat(runDir); err != nil {
		t.Fatalf("run directory removed after an unclean disconnect: %v", err)
	}
}

// TestRemoteScriptShutdownStopsServiceImmediately covers the explicit-close
// path: runningProcess.stop writes SHUTDOWN, so even with a long grace window
// the keeper stops the service and removes the run directory at once.
func TestRemoteScriptShutdownStopsServiceImmediately(t *testing.T) {
	requireProcessTools(t)
	runDir := makeRemoteRunDir(t)
	process, _, pgid := startSupervisedFakeService(t, runDir, 30*time.Second, 200*time.Millisecond)

	started := time.Now()
	process.stop()
	if elapsed := time.Since(started); elapsed >= 2*time.Second {
		t.Fatalf("SHUTDOWN close took %s; attached shell did not exit promptly", elapsed)
	}
	waitForServiceStopped(t, pgid, 3*time.Second)
	waitForRunDirRemoved(t, runDir, 3*time.Second)
}

// TestRemoteScriptGraceExpiryStopsService covers the timeout path: after an
// unclean drop the keeper stops the service and removes the run directory once
// the grace window of free-lease probes elapses.
func TestRemoteScriptGraceExpiryStopsService(t *testing.T) {
	requireProcessTools(t)
	runDir := makeRemoteRunDir(t)
	process, writer, pgid := startSupervisedFakeService(t, runDir, 400*time.Millisecond, 200*time.Millisecond)

	if err := writer.Close(); err != nil { // unclean drop opens the grace window
		t.Fatal(err)
	}
	select {
	case <-process.done:
	case <-time.After(2 * time.Second):
		t.Fatal("attached shell did not exit on stdin EOF")
	}
	waitForServiceStopped(t, pgid, 3*time.Second)
	waitForRunDirRemoved(t, runDir, 3*time.Second)
}

// TestReattachScriptHoldsLeaseWhenSocketPresent verifies the re-attach script
// re-acquires the lease when the Unix socket still exists and then blocks
// watching stdin, stopping only on an explicit SHUTDOWN.
func TestReattachScriptHoldsLeaseWhenSocketPresent(t *testing.T) {
	requireProcessTools(t)
	runDir := makeRemoteRunDir(t)
	socketPath := filepath.Join(runDir, "j.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	command := exec.Command("sh", "-c", reattachScript(runDir, socketPath))
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	command.Stdin = reader
	configureCommand(command)
	if err := command.Start(); err != nil {
		_ = reader.Close()
		_ = writer.Close()
		t.Fatal(err)
	}
	_ = reader.Close()
	process := newRunningProcess(command, writer, nil)
	t.Cleanup(func() {
		_ = writer.Close()
		select {
		case <-process.done:
		case <-time.After(2 * time.Second):
		}
	})

	assertProcessRunning(t, process)
	if !leaseHeld(t, filepath.Join(runDir, "lease")) {
		t.Fatal("re-attach did not hold the lease while the socket was present")
	}

	if _, err := io.WriteString(writer, "SHUTDOWN\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-process.done:
	case <-time.After(2 * time.Second):
		t.Fatal("re-attach did not exit on SHUTDOWN")
	}
	if _, err := os.Stat(filepath.Join(runDir, "shutdown")); err != nil {
		t.Fatalf("re-attach did not create the shutdown marker: %v", err)
	}
}

// TestReattachScriptFailsWhenSocketMissing verifies the re-attach script exits
// nonzero when the JupyterLab Unix socket is gone, so the caller leaves the
// instance dead instead of forwarding to nothing.
func TestReattachScriptFailsWhenSocketMissing(t *testing.T) {
	requireProcessTools(t)
	runDir := makeRemoteRunDir(t)
	socketPath := filepath.Join(runDir, "j.sock") // never created

	command := exec.Command("sh", "-c", reattachScript(runDir, socketPath))
	err := command.Run()
	if err == nil {
		t.Fatal("re-attach succeeded without a live Unix socket")
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("re-attach failed with an unexpected error: %v", err)
	}
}

// waitForWatcherPID polls watcher.pid until it records wantPid or the timeout
// elapses, so eviction tests can synchronize on the recorded lease holder.
func waitForWatcherPID(t *testing.T, watcherPath string, wantPid int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(watcherPath); err == nil {
			if pid, convErr := strconv.Atoi(strings.TrimSpace(string(data))); convErr == nil && pid == wantPid {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("watcher.pid did not record %d within %s", wantPid, timeout)
}

// TestReattachScriptEvictsStaleWatcher covers the reconnect defect: after a
// network drop the old attached shell can survive behind a dead TCP peer and
// keep holding the lease. Re-attach must evict that stale watcher (recorded in
// watcher.pid) so a fresh attachment always wins the lease instead of 502ing.
func TestReattachScriptEvictsStaleWatcher(t *testing.T) {
	requireProcessTools(t)
	runDir := makeRemoteRunDir(t)
	socketPath := filepath.Join(runDir, "j.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	leasePath := filepath.Join(runDir, "lease")
	watcherPath := filepath.Join(runDir, "watcher.pid")
	// A stale watcher mimicking the dead-peer attached shell: it grabs the
	// lease on fd 9, records its pid, then sleeps far longer than the test.
	stale := `exec 9>` + shellQuote(leasePath) + `
flock 9
printf '%s\n' "$$" > ` + shellQuote(watcherPath) + `
exec sleep 300`
	staleCommand := exec.Command("sh", "-c", stale)
	configureCommand(staleCommand)
	if err := staleCommand.Start(); err != nil {
		t.Fatal(err)
	}
	stalePid := staleCommand.Process.Pid
	staleDone := make(chan error, 1)
	go func() { staleDone <- staleCommand.Wait() }()
	t.Cleanup(func() { _ = syscall.Kill(stalePid, syscall.SIGKILL) })

	waitForWatcherPID(t, watcherPath, stalePid, 3*time.Second)
	if !leaseHeld(t, leasePath) {
		t.Fatal("stale watcher did not take the lease")
	}

	command := exec.Command("sh", "-c", reattachScript(runDir, socketPath))
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	command.Stdin = reader
	configureCommand(command)
	if err := command.Start(); err != nil {
		_ = reader.Close()
		_ = writer.Close()
		t.Fatal(err)
	}
	_ = reader.Close()
	process := newRunningProcess(command, writer, nil)
	t.Cleanup(func() {
		_ = writer.Close()
		select {
		case <-process.done:
		case <-time.After(2 * time.Second):
		}
	})

	// (b) the stale holder is killed, which is the only way it releases the
	// lease it would otherwise sleep on for 300s.
	select {
	case werr := <-staleDone:
		var staleExit *exec.ExitError
		if !errors.As(werr, &staleExit) {
			t.Fatalf("stale watcher exited without an error status: %v", werr)
		}
		status, ok := staleExit.Sys().(syscall.WaitStatus)
		if !ok || !status.Signaled() {
			t.Fatalf("stale watcher was not terminated by a signal: %v", werr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("re-attach did not evict the stale watcher")
	}

	// (c) watcher.pid now records the re-attach shell, written only after its
	// flock succeeds, so this also proves (a) it holds the lease.
	waitForWatcherPID(t, watcherPath, command.Process.Pid, 3*time.Second)
	assertProcessRunning(t, process)
	if !leaseHeld(t, leasePath) {
		t.Fatal("re-attach did not hold the lease after evicting the stale watcher")
	}

	// The success path ends cleanly on an explicit shutdown, exactly like the
	// attached launch shell.
	if _, err := io.WriteString(writer, "SHUTDOWN\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-process.done:
	case <-time.After(2 * time.Second):
		t.Fatal("re-attach did not exit on SHUTDOWN")
	}
	if _, err := os.Stat(filepath.Join(runDir, "shutdown")); err != nil {
		t.Fatalf("re-attach did not create the shutdown marker: %v", err)
	}
}

func TestLocalStopTerminatesProcessGroup(t *testing.T) {
	command := exec.Command("sh", "-c", "sleep 30 & wait")
	configureCommand(command)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	processGroup := command.Process.Pid
	process := newRunningProcess(command, nil, nil)
	assertProcessRunning(t, process)
	process.stop()

	deadline := time.Now().Add(time.Second)
	for {
		err := syscall.Kill(-processGroup, 0)
		if errors.Is(err, syscall.ESRCH) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Jupyter process group %d survived shutdown: %v", processGroup, err)
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

func TestRemoteRuntimeUsesShort128BitNoncePath(t *testing.T) {
	runDirectory, socketPath, err := newRemoteRuntime()
	if err != nil {
		t.Fatal(err)
	}
	if err := validateRemoteRuntime(runDirectory, socketPath); err != nil {
		t.Fatal(err)
	}
	nonce := strings.TrimPrefix(runDirectory, remoteRunPrefix)
	if len(nonce) != remoteNonceBytes*2 {
		t.Fatalf("runtime nonce length = %d, want %d", len(nonce), remoteNonceBytes*2)
	}
	if len(socketPath) > 100 {
		t.Fatalf("remote socket path is too long: %q", socketPath)
	}
}

func TestRemoteBootstrapCreatesPrivateIPCConfig(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not installed")
	}
	probe := exec.Command(
		python,
		"-c",
		"import jupyterlab, jupyter_client, jupyter_server, zmq",
	)
	if output, probeErr := probe.CombinedOutput(); probeErr != nil {
		t.Skipf("python3 does not provide the Jupyter test dependencies: %s", output)
	}

	runDirectory, socketPath, err := newRemoteRuntime()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(runDirectory); err != nil {
			t.Errorf("remove remote test runtime: %v", err)
		}
	})
	configuration := remoteLaunchConfig{
		ContentsManager: contentsManagerPython,
		Directory:       t.TempDir(),
		PathPrefix:      "/jupyter/test/",
		RunDirectory:    runDirectory,
		SocketPath:      socketPath,
		Token:           "private-test-token",
	}
	frame, err := remoteConfigFrame(configuration)
	if err != nil {
		t.Fatal(err)
	}
	bootstrap := exec.Command(python, "-c", remoteBootstrapPython, runDirectory, socketPath)
	bootstrap.Stdin = bytes.NewReader(frame)
	if output, bootstrapErr := bootstrap.CombinedOutput(); bootstrapErr != nil {
		t.Fatalf("remote bootstrap failed: %v\n%s", bootstrapErr, output)
	}

	runtimeInfo, err := os.Stat(runDirectory)
	if err != nil {
		t.Fatal(err)
	}
	if mode := runtimeInfo.Mode().Perm(); mode != 0o700 {
		t.Fatalf("runtime mode = %04o, want 0700", mode)
	}
	configPath := filepath.Join(runDirectory, "jupyter_server_config.json")
	configInfo, err := os.Stat(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if mode := configInfo.Mode().Perm(); mode != 0o600 {
		t.Fatalf("config mode = %04o, want 0600", mode)
	}
	modulePath := filepath.Join(runDirectory, contentsManagerModule+".py")
	moduleInfo, err := os.Stat(modulePath)
	if err != nil {
		t.Fatal(err)
	}
	if mode := moduleInfo.Mode().Perm(); mode != 0o600 {
		t.Fatalf("contents manager mode = %04o, want 0600", mode)
	}
	moduleSource, err := os.ReadFile(modulePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(moduleSource) != contentsManagerPython {
		t.Fatal("remote contents manager does not match the bundled source")
	}
	payload, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var serverConfig map[string]map[string]any
	if err := json.Unmarshal(payload, &serverConfig); err != nil {
		t.Fatal(err)
	}
	if got := serverConfig["IdentityProvider"]["token"]; got != configuration.Token {
		t.Fatalf("configured token = %#v, want %q", got, configuration.Token)
	}
	if got := serverConfig["ServerApp"]["root_dir"]; got != configuration.Directory {
		t.Fatalf("configured root = %#v, want %q", got, configuration.Directory)
	}
	if got := serverConfig["KernelManager"]["transport"]; got != "ipc" {
		t.Fatalf("configured kernel transport = %#v, want ipc", got)
	}
	if encryption, ok := serverConfig["KernelManager"]["transport_encryption"]; ok {
		if encryption != "auto" || serverConfig["MappingKernelManager"]["transport_encryption"] != "auto" {
			t.Fatalf("inconsistent Curve configuration: %#v", serverConfig)
		}
	}
}

func TestRemoteBootstrapFeatureProbeFailsBeforeRuntimeCreation(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not installed")
	}
	runDirectory, socketPath, err := newRemoteRuntime()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(runDirectory); err != nil {
			t.Errorf("remove remote test runtime: %v", err)
		}
	})
	configuration := remoteLaunchConfig{
		ContentsManager: contentsManagerPython,
		Directory:       t.TempDir(),
		PathPrefix:      "/jupyter/test/",
		RunDirectory:    runDirectory,
		SocketPath:      socketPath,
		Token:           "private-test-token",
	}
	frame, err := remoteConfigFrame(configuration)
	if err != nil {
		t.Fatal(err)
	}
	const blockJupyterImport = `
import builtins

original_import = builtins.__import__


def blocked_import(name, globals=None, locals=None, fromlist=(), level=0):
    if name == "jupyterlab":
        raise ImportError("blocked by open-server test")
    return original_import(name, globals, locals, fromlist, level)


builtins.__import__ = blocked_import
`
	bootstrap := exec.Command(
		python,
		"-c",
		blockJupyterImport+remoteBootstrapPython,
		runDirectory,
		socketPath,
	)
	bootstrap.Stdin = bytes.NewReader(frame)
	output, bootstrapErr := bootstrap.CombinedOutput()
	if bootstrapErr == nil {
		t.Fatalf("remote bootstrap passed a failed feature probe:\n%s", output)
	}
	if !strings.Contains(string(output), "security feature probe failed") {
		t.Fatalf("remote bootstrap returned the wrong diagnostic: %s", output)
	}
	if _, statErr := os.Lstat(runDirectory); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("failed feature probe created its runtime: %v", statErr)
	}
}

func TestRemoteBootstrapFailureRemovesCreatedRuntime(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not installed")
	}
	probe := exec.Command(
		python,
		"-c",
		"import jupyterlab, jupyter_client, jupyter_server, zmq",
	)
	if output, probeErr := probe.CombinedOutput(); probeErr != nil {
		t.Skipf("python3 does not provide the Jupyter test dependencies: %s", output)
	}

	runDirectory, socketPath, err := newRemoteRuntime()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(runDirectory); err != nil {
			t.Errorf("remove remote test runtime: %v", err)
		}
	})
	configuration := remoteLaunchConfig{
		ContentsManager: contentsManagerPython,
		Directory:       t.TempDir(),
		KernelName:      "open-server-test",
		KernelPython:    filepath.Join(t.TempDir(), "missing-python"),
		PathPrefix:      "/jupyter/test/",
		RunDirectory:    runDirectory,
		SocketPath:      socketPath,
		Token:           "private-test-token",
	}
	frame, err := remoteConfigFrame(configuration)
	if err != nil {
		t.Fatal(err)
	}
	bootstrap := exec.Command(python, "-c", remoteBootstrapPython, runDirectory, socketPath)
	bootstrap.Stdin = bytes.NewReader(frame)
	if output, bootstrapErr := bootstrap.CombinedOutput(); bootstrapErr == nil {
		t.Fatalf("remote bootstrap unexpectedly succeeded:\n%s", output)
	}
	if _, statErr := os.Lstat(runDirectory); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("failed bootstrap left its runtime behind: %v", statErr)
	}
}
