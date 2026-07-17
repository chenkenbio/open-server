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
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRemoteTensorBoardScriptStopsOnStdinEOF(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable")
	}
	runtimeDirectory := filepath.Join(t.TempDir(), "runtime")
	remoteSocket := filepath.Join(runtimeDirectory, "tensorboard.sock")
	helper := "import sys\nsys.stdin.buffer.read()\n"
	command := exec.Command("sh", "-c", remoteTensorBoardScript(python, runtimeDirectory, remoteSocket, helper))
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	command.Stdin = reader
	if err := command.Start(); err != nil {
		_ = reader.Close()
		_ = writer.Close()
		t.Fatal(err)
	}
	_ = reader.Close()
	process := newRunningProcess(command, writer)
	assertProcessRunning(t, process)
	if info, err := os.Stat(runtimeDirectory); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("remote runtime mode = %v, error = %v; want 0700", infoMode(info), err)
	}
	if info, err := os.Stat(filepath.Join(runtimeDirectory, "helper.py")); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("remote helper mode = %v, error = %v; want 0600", infoMode(info), err)
	}

	started := time.Now()
	process.stop()
	if elapsed := time.Since(started); elapsed >= 2*time.Second {
		t.Fatalf("control-pipe shutdown took %s; remote wrapper did not exit promptly", elapsed)
	}
	if _, err := os.Stat(runtimeDirectory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("remote runtime survived shutdown: %v", err)
	}
}

func infoMode(info os.FileInfo) os.FileMode {
	if info == nil {
		return 0
	}
	return info.Mode().Perm()
}

func TestRemoteHelperServesAuthenticatedUnixSocketAndCleansUp(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable")
	}
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
	finished := false
	defer func() {
		if !finished {
			_ = stdin.Close()
			_ = command.Process.Kill()
			_ = command.Wait()
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

	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case err := <-done:
		readiness.Flush()
		diagnostic.Flush()
		if err != nil {
			t.Fatalf("helper exit: %v; output: %s", err, diagnostic.String())
		}
		finished = true
	case <-time.After(5 * time.Second):
		t.Fatalf("helper did not stop on stdin EOF: %s", diagnostic.String())
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
