package tensorboard

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Instance struct {
	Target  *url.URL
	Handler http.Handler
	Token   string
}

type Launcher interface {
	Start(logDirectory, pathPrefix string) (*Instance, error)
}

type ProcessLauncher struct {
	remote    bool
	rsh       string
	host      string
	python    string
	output    io.Writer
	mu        sync.Mutex
	processes []*runningProcess
	closed    bool
}

const remoteReadySentinel = "OPEN_SERVER_TENSORBOARD_READY\n"

func NewLocal(pythonInterpreter string, output io.Writer) *ProcessLauncher {
	return &ProcessLauncher{python: pythonInterpreter, output: output}
}

func NewRemote(rsh, host, pythonInterpreter string, output io.Writer) *ProcessLauncher {
	return &ProcessLauncher{remote: true, rsh: rsh, host: host, python: pythonInterpreter, output: output}
}

func (l *ProcessLauncher) Start(logDirectory, pathPrefix string) (*Instance, error) {
	const attempts = 5
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		instance, err := l.startOnce(logDirectory, pathPrefix)
		if err == nil {
			return instance, nil
		}
		lastErr = err
		if !retryablePortError(err) {
			break
		}
	}
	return nil, lastErr
}

func (l *ProcessLauncher) startOnce(logDirectory, pathPrefix string) (*Instance, error) {
	if logDirectory == "" || !strings.HasPrefix(pathPrefix, "/tensorboard/") {
		return nil, errors.New("invalid TensorBoard launch request")
	}
	localPort, err := pickLocalPort()
	if err != nil {
		return nil, err
	}

	var command *exec.Cmd
	var remoteFrame []byte
	token := ""
	if l.remote {
		token, err = randomHex(32)
		if err != nil {
			return nil, fmt.Errorf("generate TensorBoard bearer token: %w", err)
		}
		nonce, err := randomHex(16)
		if err != nil {
			return nil, fmt.Errorf("generate TensorBoard runtime name: %w", err)
		}
		runtimeDirectory := "/tmp/open-server-tb-" + nonce
		remoteSocket := runtimeDirectory + "/tensorboard.sock"
		executable, arguments, err := remoteCommand(
			l.rsh, l.host, l.python, localPort, runtimeDirectory, remoteSocket,
		)
		if err != nil {
			return nil, err
		}
		remoteFrame, err = remoteConfigurationFrame(remoteConfiguration{
			Version:      1,
			LogDirectory: logDirectory,
			PathPrefix:   pathPrefix,
			SocketPath:   remoteSocket,
			Token:        token,
		})
		if err != nil {
			return nil, err
		}
		command = exec.Command(executable, arguments...)
	} else {
		executable, arguments, err := tensorBoardCommand(l.python, logDirectory, pathPrefix, localPort)
		if err != nil {
			return nil, err
		}
		command = exec.Command(executable, arguments...)
	}
	var stdinReader, stdinWriter *os.File
	if l.remote {
		stdinReader, stdinWriter, err = os.Pipe()
		if err != nil {
			return nil, fmt.Errorf("create TensorBoard SSH control pipe: %w", err)
		}
		command.Stdin = stdinReader
	}
	configureCommand(command)
	diagnostic := newCaptureWriter(l.output, token)
	var readiness *readinessWriter
	if l.remote {
		readiness = newReadinessWriter(diagnostic, remoteReadySentinel)
		command.Stdout = readiness
	} else {
		command.Stdout = diagnostic
	}
	command.Stderr = diagnostic
	if err := command.Start(); err != nil {
		if stdinReader != nil {
			_ = stdinReader.Close()
			_ = stdinWriter.Close()
		}
		return nil, fmt.Errorf("start TensorBoard: %w", err)
	}
	if stdinReader != nil {
		_ = stdinReader.Close()
	}
	process := newRunningProcess(command, stdinWriter, func() {
		if readiness != nil {
			readiness.Flush()
		}
		diagnostic.Flush()
	})
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		process.stop()
		return nil, errors.New("TensorBoard launcher is closed")
	}
	l.processes = append(l.processes, process)
	l.mu.Unlock()
	if stdinWriter != nil {
		written, writeErr := stdinWriter.Write(remoteFrame)
		if writeErr == nil && written != len(remoteFrame) {
			writeErr = io.ErrShortWrite
		}
		if writeErr != nil {
			process.stop()
			return nil, fmt.Errorf("send TensorBoard configuration through SSH: %w", writeErr)
		}
	}
	if readiness != nil {
		readyTimer := time.NewTimer(15 * time.Second)
		defer readyTimer.Stop()
		select {
		case <-readiness.Ready():
		case <-process.done:
			message := strings.TrimSpace(diagnostic.String())
			if message != "" {
				return nil, fmt.Errorf("TensorBoard exited before becoming ready: %s", message)
			}
			return nil, errors.New("TensorBoard exited before becoming ready")
		case <-readyTimer.C:
			process.stop()
			return nil, errors.New("TensorBoard did not create its private socket within 15 seconds")
		}
	}

	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(localPort))
	target, _ := url.Parse("http://" + address)
	probeURL := target.String() + pathPrefix + "/"
	probeClient := &http.Client{Timeout: 500 * time.Millisecond}
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		request, requestErr := http.NewRequest(http.MethodGet, probeURL, nil)
		if requestErr != nil {
			process.stop()
			return nil, fmt.Errorf("create TensorBoard readiness request: %w", requestErr)
		}
		if token != "" {
			request.Header.Set("Authorization", "Bearer "+token)
		}
		response, probeErr := probeClient.Do(request)
		if probeErr == nil {
			_ = response.Body.Close()
			if response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusBadRequest {
				return &Instance{Target: target, Token: token}, nil
			}
		}
		select {
		case <-process.done:
			message := strings.TrimSpace(diagnostic.String())
			if message != "" {
				return nil, fmt.Errorf("TensorBoard exited before becoming ready: %s", message)
			}
			return nil, errors.New("TensorBoard exited before becoming ready")
		case <-deadline.C:
			process.stop()
			return nil, errors.New("TensorBoard did not become ready within 15 seconds")
		case <-ticker.C:
		}
	}
}

func retryablePortError(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "address already in use") ||
		(strings.Contains(message, "port") && strings.Contains(message, "in use")) ||
		strings.Contains(message, "forwarding failed") ||
		strings.Contains(message, "cannot listen to port")
}

func (l *ProcessLauncher) Close() {
	l.mu.Lock()
	l.closed = true
	processes := append([]*runningProcess(nil), l.processes...)
	l.mu.Unlock()
	for _, process := range processes {
		process.stop()
	}
}

func localArguments(logDirectory, pathPrefix string, port int) []string {
	return []string{
		"--logdir", logDirectory,
		"--host", "127.0.0.1",
		"--port", strconv.Itoa(port),
		"--path_prefix", pathPrefix,
	}
}

func tensorBoardCommand(pythonInterpreter, logDirectory, pathPrefix string, port int) (string, []string, error) {
	arguments := localArguments(logDirectory, pathPrefix, port)
	if pythonInterpreter == "" {
		return "tensorboard", arguments, nil
	}
	if strings.HasPrefix(pythonInterpreter, "-") || strings.ContainsAny(pythonInterpreter, "\x00\r\n") {
		return "", nil, errors.New("invalid Python interpreter")
	}
	return pythonInterpreter, append([]string{"-m", "tensorboard.main"}, arguments...), nil
}

func remoteCommand(rsh, host, pythonInterpreter string, localPort int, runtimeDirectory, remoteSocket string) (string, []string, error) {
	if rsh == "" {
		rsh = "ssh"
	}
	if host == "" || strings.HasPrefix(host, "-") || strings.ContainsAny(host, "\x00\r\n") {
		return "", nil, errors.New("invalid SSH host for TensorBoard")
	}
	if strings.HasPrefix(pythonInterpreter, "-") || strings.ContainsAny(pythonInterpreter, "\x00\r\n") {
		return "", nil, errors.New("invalid Python interpreter")
	}
	if err := validateRemoteRuntime(runtimeDirectory, remoteSocket); err != nil {
		return "", nil, err
	}
	forward := fmt.Sprintf("127.0.0.1:%d:%s", localPort, remoteSocket)
	arguments := []string{
		"-T", "-x", "-a",
		"-o", "ExitOnForwardFailure=yes",
		"-o", "ForkAfterAuthentication=no",
		"-o", "ControlMaster=no",
		"-o", "ControlPersist=no",
		"-o", "PermitLocalCommand=no",
		"-o", "RemoteCommand=none",
		"-o", "SessionType=default",
		"-o", "StdinNull=no",
		"-L", forward,
		host,
		remoteTensorBoardScript(pythonInterpreter, runtimeDirectory, remoteSocket, remoteTensorBoardHelper),
	}
	return rsh, arguments, nil
}

func validateRemoteRuntime(runtimeDirectory, remoteSocket string) error {
	const prefix = "/tmp/open-server-tb-"
	nonce := strings.TrimPrefix(runtimeDirectory, prefix)
	if nonce == runtimeDirectory || len(nonce) != 32 || remoteSocket != runtimeDirectory+"/tensorboard.sock" {
		return errors.New("invalid remote TensorBoard runtime path")
	}
	if _, err := hex.DecodeString(nonce); err != nil {
		return errors.New("invalid remote TensorBoard runtime path")
	}
	return nil
}

func remoteTensorBoardScript(pythonInterpreter, runtimeDirectory, remoteSocket, helperSource string) string {
	helperPath := runtimeDirectory + "/helper.py"
	lines := []string{
		"helper_pid=",
		"runtime_created=",
		"runtime_dir=" + shellQuote(runtimeDirectory),
		"socket_path=" + shellQuote(remoteSocket),
		"helper_path=" + shellQuote(helperPath),
		"cleanup() {",
		"  trap - EXIT HUP INT TERM",
		`  if [ -n "$helper_pid" ] && kill -0 -"$helper_pid" 2>/dev/null; then`,
		`    kill -TERM -"$helper_pid" 2>/dev/null || :`,
		"    sleep 0.5",
		`    kill -KILL -"$helper_pid" 2>/dev/null || :`,
		"  fi",
		`  if [ -n "$helper_pid" ]; then wait "$helper_pid" 2>/dev/null || :; fi`,
		`  if [ -n "$runtime_created" ]; then`,
		`    rm -f "$socket_path" "$helper_path" 2>/dev/null || :`,
		`    rmdir "$runtime_dir" 2>/dev/null || :`,
		"  fi",
		"}",
		"trap cleanup EXIT",
		"trap 'exit 143' HUP INT TERM",
		"umask 077",
	}
	if pythonInterpreter == "" {
		lines = append(lines,
			`tensorboard_executable=$(command -v tensorboard) || { echo "TensorBoard was not found on remote PATH; pass -py" >&2; exit 127; }`,
			`case "$tensorboard_executable" in /*) ;; *) echo "TensorBoard launcher is not a regular executable path; pass -py" >&2; exit 127 ;; esac`,
			`IFS= read -r tensorboard_shebang < "$tensorboard_executable" || { echo "Could not read TensorBoard launcher; pass -py" >&2; exit 127; }`,
			`case "$tensorboard_shebang" in '#!'/*) python_executable=${tensorboard_shebang#\#!} ;; *) echo "TensorBoard launcher has an unsupported shebang; pass -py" >&2; exit 127 ;; esac`,
			`case "$python_executable" in *[[:space:]]*) echo "TensorBoard launcher has an unsupported shebang; pass -py" >&2; exit 127 ;; esac`,
			`[ -x "$python_executable" ] || { echo "TensorBoard Python interpreter is not executable; pass -py" >&2; exit 127; }`,
		)
	} else {
		lines = append(lines, "python_executable="+shellQuote(pythonInterpreter))
	}
	lines = append(lines,
		`mkdir "$runtime_dir" || { echo "Could not create private TensorBoard runtime directory" >&2; exit 1; }`,
		"runtime_created=1",
		`printf '%s' `+shellQuote(helperSource)+` > "$helper_path" || exit 1`,
		`chmod 600 "$helper_path" || exit 1`,
		"exec 3<&0",
		`setsid "$python_executable" -B "$helper_path" <&3 &`,
		"helper_pid=$!",
		"exec 3<&-",
		`wait "$helper_pid"`,
		"status=$?",
		`exit "$status"`,
	)
	return strings.Join(lines, "\n")
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

type remoteConfiguration struct {
	Version      int    `json:"version"`
	LogDirectory string `json:"log_directory"`
	PathPrefix   string `json:"path_prefix"`
	SocketPath   string `json:"socket_path"`
	Token        string `json:"token"`
}

func remoteConfigurationFrame(configuration remoteConfiguration) ([]byte, error) {
	payload, err := json.Marshal(configuration)
	if err != nil {
		return nil, fmt.Errorf("encode remote TensorBoard configuration: %w", err)
	}
	if len(payload) > 1<<20 {
		return nil, errors.New("remote TensorBoard configuration is too large")
	}
	frame := make([]byte, 4+len(payload))
	binary.BigEndian.PutUint32(frame[:4], uint32(len(payload)))
	copy(frame[4:], payload)
	return frame, nil
}

func randomHex(size int) (string, error) {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

const remoteTensorBoardHelper = `import hmac
import json
import os
import signal
import socket
import socketserver
import stat
import struct
import sys
import threading
from wsgiref.simple_server import WSGIRequestHandler, WSGIServer

from tensorboard import program


_MAX_CONFIGURATION_SIZE = 1 << 20


def _read_exact(stream, size):
    """Read exactly size bytes from a binary stream."""
    chunks = []
    remaining = size
    while remaining:
        chunk = stream.read(remaining)
        if not chunk:
            raise RuntimeError("incomplete TensorBoard configuration")
        chunks.append(chunk)
        remaining -= len(chunk)
    return b"".join(chunks)


def _read_configuration(stream):
    """Read and validate the length-prefixed launch configuration."""
    size = struct.unpack("!I", _read_exact(stream, 4))[0]
    if size == 0 or size > _MAX_CONFIGURATION_SIZE:
        raise RuntimeError("invalid TensorBoard configuration size")
    try:
        configuration = json.loads(_read_exact(stream, size).decode("utf-8"))
    except (UnicodeDecodeError, json.JSONDecodeError) as error:
        raise RuntimeError("invalid TensorBoard configuration") from error
    if configuration.get("version") != 1:
        raise RuntimeError("unsupported TensorBoard configuration version")
    required = ("log_directory", "path_prefix", "socket_path", "token")
    if any(not isinstance(configuration.get(key), str) for key in required):
        raise RuntimeError("invalid TensorBoard configuration fields")
    return configuration


def _validate_configuration(configuration):
    """Validate paths, token, and private runtime ownership."""
    runtime_directory = os.path.abspath(os.path.dirname(__file__))
    runtime_info = os.lstat(runtime_directory)
    if not stat.S_ISDIR(runtime_info.st_mode):
        raise RuntimeError("TensorBoard runtime path is not a directory")
    if runtime_info.st_uid != os.geteuid():
        raise RuntimeError("TensorBoard runtime directory has the wrong owner")
    if stat.S_IMODE(runtime_info.st_mode) != 0o700:
        raise RuntimeError("TensorBoard runtime directory is not mode 0700")

    socket_path = configuration["socket_path"]
    expected_socket = os.path.join(runtime_directory, "tensorboard.sock")
    if socket_path != expected_socket:
        raise RuntimeError("invalid TensorBoard socket path")
    if os.path.lexists(socket_path):
        raise RuntimeError("TensorBoard socket path already exists")

    log_directory = configuration["log_directory"]
    if not log_directory or "\x00" in log_directory:
        raise RuntimeError("invalid TensorBoard log directory")
    path_prefix = configuration["path_prefix"]
    required_prefix = "/tensorboard/"
    identifier = (
        path_prefix[len(required_prefix):]
        if path_prefix.startswith(required_prefix)
        else path_prefix
    )
    if (
        not path_prefix.startswith(required_prefix)
        or not identifier
        or any(
            not (character.isalnum() or character in "-_")
            for character in identifier
        )
    ):
        raise RuntimeError("invalid TensorBoard path prefix")
    token = configuration["token"]
    if len(token) != 64 or any(
        character not in "0123456789abcdef" for character in token
    ):
        raise RuntimeError("invalid TensorBoard bearer token")


class _BearerAuth:
    """Require the per-launch bearer token before invoking TensorBoard."""

    def __init__(self, application, token):
        """Store the protected WSGI application and expected credential."""
        self._application = application
        self._authorization = "Bearer " + token

    def __call__(self, environ, start_response):
        """Authenticate one request without exposing the token downstream."""
        authorization = environ.get("HTTP_AUTHORIZATION", "")
        if not hmac.compare_digest(authorization, self._authorization):
            body = b"Unauthorized\n"
            start_response(
                "401 Unauthorized",
                [
                    ("Content-Type", "text/plain; charset=utf-8"),
                    ("Content-Length", str(len(body))),
                    ("WWW-Authenticate", "Bearer"),
                    ("Cache-Control", "no-store"),
                ],
            )
            return [body]
        environ.pop("HTTP_AUTHORIZATION", None)
        return self._application(environ, start_response)


class _UnixRequestHandler(WSGIRequestHandler):
    """Adapt the standard WSGI request handler to Unix-socket clients."""

    def address_string(self):
        """Return a stable non-network client label."""
        return "local"

    def log_message(self, format_string, *arguments):
        """Suppress per-request logs that could expose request paths."""
        del format_string, arguments


class _ThreadedUnixWSGIServer(socketserver.ThreadingMixIn, WSGIServer):
    """Serve a WSGI application on a private Unix-domain socket."""

    address_family = socket.AF_UNIX
    allow_reuse_address = False
    daemon_threads = True
    block_on_close = False

    def server_bind(self):
        """Bind AF_UNIX while supplying the metadata expected by WSGI."""
        socketserver.TCPServer.server_bind(self)
        self.server_name = "localhost"
        self.server_port = 0
        self.setup_environ()

    def get_request(self):
        """Return a synthetic address compatible with WSGIRequestHandler."""
        request, _ = self.socket.accept()
        return request, ("local", 0)


class _TensorBoardUnixServer(_ThreadedUnixWSGIServer):
    """Implement TensorBoard's custom server contract over AF_UNIX."""

    def __init__(self, socket_path, application, flags, token):
        """Bind the private socket and install bearer authentication."""
        self._flags = flags
        self.done = threading.Event()
        super().__init__(socket_path, _UnixRequestHandler)
        try:
            os.chmod(socket_path, 0o600)
            self.set_app(_BearerAuth(application, token))
        except BaseException:
            self.server_close()
            raise

    def get_url(self):
        """Return a syntactically valid URL for TensorBoard.launch()."""
        prefix = self._flags.path_prefix.rstrip("/")
        return "http://localhost%s/" % prefix

    def serve_forever(self, poll_interval=0.5):
        """Record termination so the foreground helper can fail closed."""
        try:
            super().serve_forever(poll_interval=poll_interval)
        finally:
            self.done.set()


def _force_private_ingestion(flags):
    """Disable TensorBoard data-provider modes that open TCP listeners."""
    if hasattr(flags, "load_fast"):
        flags.load_fast = "false"
        if flags.load_fast != "false":
            raise RuntimeError("could not disable TensorBoard fast data server")
    if hasattr(flags, "grpc_data_provider"):
        flags.grpc_data_provider = ""
        if flags.grpc_data_provider:
            raise RuntimeError("could not disable TensorBoard gRPC data provider")


def _watch_stdin(stream, stop_event):
    """Treat SSH stdin EOF as revocation of the helper's lifetime lease."""
    try:
        while stream.read(65536):
            pass
    finally:
        stop_event.set()


def _stop_server(server):
    """Stop request handling and close the Unix listener."""
    if not server.done.is_set():
        server.shutdown()
    server.server_close()


def _run(configuration):
    """Configure TensorBoard and supervise it until SSH revokes the lease."""
    _validate_configuration(configuration)
    socket_path = configuration["socket_path"]
    token = configuration["token"]
    server_holder = {}
    stop_event = threading.Event()

    def server_factory(application, flags):
        """Construct and retain TensorBoard's private Unix server."""
        server = _TensorBoardUnixServer(
            socket_path, application, flags, token
        )
        server_holder["server"] = server
        return server

    def request_stop(_signal_number, _frame):
        """Convert process signals into an orderly foreground shutdown."""
        stop_event.set()

    for signal_number in (signal.SIGHUP, signal.SIGINT, signal.SIGTERM):
        signal.signal(signal_number, request_stop)

    tensorboard = program.TensorBoard(server_class=server_factory)
    tensorboard.configure(
        argv=[
            "open-server-tensorboard",
            "--logdir",
            configuration["log_directory"],
            "--path_prefix",
            configuration["path_prefix"],
        ]
    )
    _force_private_ingestion(tensorboard.flags)

    server = None
    try:
        tensorboard.launch()
        server = server_holder.get("server")
        if server is None:
            raise RuntimeError("TensorBoard did not create the Unix server")
        if server.done.is_set():
            raise RuntimeError("TensorBoard server stopped during startup")
        sys.stdout.write("OPEN_SERVER_TENSORBOARD_READY\n")
        sys.stdout.flush()
        watcher = threading.Thread(
            target=_watch_stdin,
            args=(sys.stdin.buffer, stop_event),
            name="open-server-lease",
            daemon=True,
        )
        watcher.start()
        while not stop_event.wait(0.1):
            if server.done.is_set():
                raise RuntimeError("TensorBoard server stopped unexpectedly")
    finally:
        if server is not None:
            _stop_server(server)
        if os.path.lexists(socket_path):
            os.unlink(socket_path)


def main():
    """Read the private launch frame and run the supervised server."""
    os.umask(0o077)
    try:
        configuration = _read_configuration(sys.stdin.buffer)
        _run(configuration)
    except BaseException as error:
        sys.stderr.write("open-server TensorBoard helper failed: %s\n" % error)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
`

func pickLocalPort() (int, error) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("reserve local TensorBoard port: %w", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		return 0, fmt.Errorf("release local TensorBoard port: %w", err)
	}
	return port, nil
}

type runningProcess struct {
	command  *exec.Cmd
	stdin    io.Closer
	done     chan struct{}
	stopOnce sync.Once
}

func newRunningProcess(command *exec.Cmd, stdin io.Closer, onDone ...func()) *runningProcess {
	process := &runningProcess{command: command, stdin: stdin, done: make(chan struct{})}
	go func() {
		_ = command.Wait()
		if process.stdin != nil {
			_ = process.stdin.Close()
		}
		if len(onDone) != 0 && onDone[0] != nil {
			onDone[0]()
		}
		close(process.done)
	}()
	return process
}

func (p *runningProcess) stop() {
	p.stopOnce.Do(func() {
		select {
		case <-p.done:
			return
		default:
		}
		if p.stdin != nil {
			_ = p.stdin.Close()
			select {
			case <-p.done:
				return
			case <-time.After(3 * time.Second):
			}
		}
		_ = terminateCommand(p.command)
		select {
		case <-p.done:
		case <-time.After(3 * time.Second):
			_ = killCommand(p.command)
			<-p.done
		}
	})
}

type readinessWriter struct {
	mu       sync.Mutex
	output   io.Writer
	sentinel []byte
	pending  []byte
	ready    chan struct{}
	found    bool
}

func newReadinessWriter(output io.Writer, sentinel string) *readinessWriter {
	return &readinessWriter{
		output: output, sentinel: []byte(sentinel), ready: make(chan struct{}),
	}
}

func (w *readinessWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.found {
		_, err := w.output.Write(p)
		return len(p), err
	}
	combined := append(append([]byte(nil), w.pending...), p...)
	if index := bytes.Index(combined, w.sentinel); index >= 0 {
		if _, err := w.output.Write(combined[:index]); err != nil {
			return len(p), err
		}
		w.pending = nil
		w.found = true
		close(w.ready)
		if _, err := w.output.Write(combined[index+len(w.sentinel):]); err != nil {
			return len(p), err
		}
		return len(p), nil
	}
	hold := longestSecretPrefixSuffix(combined, w.sentinel)
	if _, err := w.output.Write(combined[:len(combined)-hold]); err != nil {
		return len(p), err
	}
	w.pending = append(w.pending[:0], combined[len(combined)-hold:]...)
	return len(p), nil
}

func (w *readinessWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.pending) != 0 {
		_, _ = w.output.Write(w.pending)
		w.pending = nil
	}
}

func (w *readinessWriter) Ready() <-chan struct{} {
	return w.ready
}

type captureWriter struct {
	mu      sync.Mutex
	buffer  bytes.Buffer
	output  io.Writer
	secret  []byte
	pending []byte
}

func newCaptureWriter(output io.Writer, secret string) *captureWriter {
	return &captureWriter{output: output, secret: []byte(secret)}
}

func (w *captureWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.secret) == 0 {
		w.append(p)
		return len(p), nil
	}
	combined := append(append([]byte(nil), w.pending...), p...)
	redacted := bytes.ReplaceAll(combined, w.secret, []byte("[redacted]"))
	hold := longestSecretPrefixSuffix(redacted, w.secret)
	toWrite := redacted[:len(redacted)-hold]
	w.pending = append(w.pending[:0], redacted[len(redacted)-hold:]...)
	w.append(toWrite)
	return len(p), nil
}

func (w *captureWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.pending) != 0 {
		w.append([]byte("[redacted]"))
		w.pending = nil
	}
}

func longestSecretPrefixSuffix(value, secret []byte) int {
	maximum := len(secret) - 1
	if maximum > len(value) {
		maximum = len(value)
	}
	for size := maximum; size > 0; size-- {
		if bytes.Equal(value[len(value)-size:], secret[:size]) {
			return size
		}
	}
	return 0
}

func (w *captureWriter) append(p []byte) {
	const limit = 32 << 10
	if len(p) >= limit {
		w.buffer.Reset()
		_, _ = w.buffer.Write(p[len(p)-limit:])
	} else {
		if w.buffer.Len()+len(p) > limit {
			old := append([]byte(nil), w.buffer.Bytes()[w.buffer.Len()+len(p)-limit:]...)
			w.buffer.Reset()
			_, _ = w.buffer.Write(old)
		}
		_, _ = w.buffer.Write(p)
	}
	if w.output != nil {
		_, _ = w.output.Write(p)
	}
}

func (w *captureWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buffer.String()
}
