//go:build !windows

package jupyter

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
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

func TestRemoteJupyterScriptStopsOnStdinEOF(t *testing.T) {
	script := supervisedRemoteScript(
		[]string{`IFS= read -r _ || exit 1`},
		"sleep 30",
		nil,
	)
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
	if _, err := writer.WriteString("lease\n"); err != nil {
		t.Fatal(err)
	}
	process := newRunningProcess(command, writer, nil)
	assertProcessRunning(t, process)

	started := time.Now()
	process.stop()
	if elapsed := time.Since(started); elapsed >= 2*time.Second {
		t.Fatalf("control-pipe shutdown took %s; remote wrapper did not exit promptly", elapsed)
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
