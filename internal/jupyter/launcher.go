// Package jupyter starts private JupyterLab servers for open-server sessions.
package jupyter

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
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
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	launchAttempts        = 5
	readinessTimeout      = 30 * time.Second
	processGracePeriod    = 3 * time.Second
	kernelRequestTimeout  = 2 * time.Second
	kernelGracePeriod     = time.Second
	remoteNonceBytes      = 16
	remoteRunPrefix       = "/tmp/osj-"
	contentsManagerModule = "open_server_jupyter"
	contentsManagerClass  = contentsManagerModule + ".WorkingDirectoryContentsManager"
)

type remoteLaunchConfig struct {
	ContentsManager string `json:"contents_manager"`
	Directory       string `json:"directory"`
	KernelName      string `json:"kernel_name"`
	KernelPython    string `json:"kernel_python"`
	PathPrefix      string `json:"path_prefix"`
	RunDirectory    string `json:"run_directory"`
	SocketPath      string `json:"socket_path"`
	Token           string `json:"token"`
}

// Instance describes a running JupyterLab server.
type Instance struct {
	Target *url.URL
	Token  string
}

// Launcher starts JupyterLab instances.
type Launcher interface {
	Start(directory, kernelPython, pathPrefix string) (*Instance, error)
}

// ProcessLauncher starts local or SSH-hosted JupyterLab processes.
type ProcessLauncher struct {
	remote       bool
	rsh          string
	host         string
	serverPython string
	output       io.Writer
	mu           sync.Mutex
	instances    []*runningInstance
	closed       bool
}

// NewLocal creates a launcher for JupyterLab on the local host.
func NewLocal(serverPython string, output io.Writer) *ProcessLauncher {
	return &ProcessLauncher{serverPython: serverPython, output: output}
}

// NewRemote creates a launcher for JupyterLab reached through SSH.
func NewRemote(rsh, host, serverPython string, output io.Writer) *ProcessLauncher {
	return &ProcessLauncher{
		remote:       true,
		rsh:          rsh,
		host:         host,
		serverPython: serverPython,
		output:       output,
	}
}

// Start launches JupyterLab rooted at directory.
func (l *ProcessLauncher) Start(directory, kernelPython, pathPrefix string) (*Instance, error) {
	if err := validateLaunch(directory, kernelPython, pathPrefix); err != nil {
		return nil, err
	}
	if err := validateExecutable(l.serverPython); err != nil {
		return nil, fmt.Errorf("invalid JupyterLab Python interpreter: %w", err)
	}
	var lastErr error
	for attempt := 0; attempt < launchAttempts; attempt++ {
		instance, err := l.startOnce(directory, kernelPython, normalizePathPrefix(pathPrefix))
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

func (l *ProcessLauncher) startOnce(directory, kernelPython, pathPrefix string) (*Instance, error) {
	localPort, err := pickLocalPort()
	if err != nil {
		return nil, err
	}
	token, err := randomHex(32)
	if err != nil {
		return nil, fmt.Errorf("generate Jupyter token: %w", err)
	}
	kernelName := ""
	if kernelPython != "" {
		identifier, identifierErr := randomHex(8)
		if identifierErr != nil {
			return nil, fmt.Errorf("generate Jupyter kernel name: %w", identifierErr)
		}
		kernelName = "open-server-" + identifier
	}

	var command *exec.Cmd
	var temporaryDirectory string
	var remoteFrame string
	var stdinReader, stdinWriter *os.File
	if l.remote {
		runDirectory, socketPath, pathErr := newRemoteRuntime()
		if pathErr != nil {
			return nil, pathErr
		}
		executable, arguments, commandErr := remoteCommand(
			l.rsh, l.host, l.serverPython, localPort, runDirectory, socketPath,
		)
		if commandErr != nil {
			return nil, commandErr
		}
		frame, frameErr := remoteConfigFrame(remoteLaunchConfig{
			ContentsManager: contentsManagerPython,
			Directory:       directory,
			KernelName:      kernelName,
			KernelPython:    kernelPython,
			PathPrefix:      pathPrefix,
			RunDirectory:    runDirectory,
			SocketPath:      socketPath,
			Token:           token,
		})
		if frameErr != nil {
			return nil, frameErr
		}
		command = exec.Command(executable, arguments...)
		stdinReader, stdinWriter, err = os.Pipe()
		if err != nil {
			return nil, fmt.Errorf("create Jupyter SSH control pipe: %w", err)
		}
		command.Stdin = stdinReader
		remoteFrame = string(frame)
	} else {
		var kernelPrefix string
		temporaryDirectory, kernelPrefix, err = prepareLocalRuntime(kernelPython, kernelName)
		if err != nil {
			return nil, err
		}
		executable, arguments, commandErr := jupyterCommand(
			l.serverPython, directory, pathPrefix, localPort, kernelName,
		)
		if commandErr != nil {
			_ = os.RemoveAll(temporaryDirectory)
			return nil, commandErr
		}
		command = exec.Command(executable, arguments...)
		command.Env = localEnvironment(token, temporaryDirectory, kernelPrefix)
	}

	configureCommand(command)
	diagnostic := newCaptureWriter(l.output, token)
	commandOutput := io.Writer(diagnostic)
	flushOutput := diagnostic.Flush
	if l.remote {
		filteredOutput := newRemoteSocketDiagnosticFilter(diagnostic)
		commandOutput = filteredOutput
		flushOutput = func() {
			filteredOutput.Flush()
			diagnostic.Flush()
		}
	}
	command.Stdout = commandOutput
	command.Stderr = commandOutput
	if err := command.Start(); err != nil {
		if stdinReader != nil {
			_ = stdinReader.Close()
			_ = stdinWriter.Close()
		}
		_ = os.RemoveAll(temporaryDirectory)
		return nil, fmt.Errorf("start JupyterLab: %w", err)
	}
	if stdinReader != nil {
		_ = stdinReader.Close()
	}
	process := newRunningProcess(command, stdinWriter, flushOutput)
	if stdinWriter != nil {
		if _, err := io.WriteString(stdinWriter, remoteFrame); err != nil {
			process.stop()
			_ = os.RemoveAll(temporaryDirectory)
			return nil, fmt.Errorf("send remote Jupyter configuration through SSH: %w", err)
		}
	}
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(localPort))
	target, _ := url.Parse("http://" + address)
	running := &runningInstance{
		process:         process,
		target:          target,
		token:           token,
		pathPrefix:      pathPrefix,
		temporaryPrefix: temporaryDirectory,
	}

	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		running.stopWithoutAPI()
		return nil, errors.New("Jupyter launcher is closed")
	}
	l.instances = append(l.instances, running)
	l.mu.Unlock()

	if err := waitUntilReady(running, diagnostic); err != nil {
		running.stopWithoutAPI()
		return nil, err
	}
	return &Instance{Target: target, Token: token}, nil
}

// Close stops all kernels and JupyterLab processes and waits for their exit.
func (l *ProcessLauncher) Close() {
	l.mu.Lock()
	l.closed = true
	instances := append([]*runningInstance(nil), l.instances...)
	l.mu.Unlock()
	for _, instance := range instances {
		instance.stop()
	}
}

func validateLaunch(directory, kernelPython, pathPrefix string) error {
	if directory == "" || strings.ContainsRune(directory, '\x00') {
		return errors.New("invalid Jupyter directory")
	}
	if err := validateExecutable(kernelPython); err != nil {
		return fmt.Errorf("invalid Jupyter kernel Python interpreter: %w", err)
	}
	prefix := strings.TrimSuffix(pathPrefix, "/")
	id := strings.TrimPrefix(prefix, "/jupyter/")
	if id == prefix || id == "" || strings.ContainsRune(id, '/') {
		return errors.New("invalid Jupyter path prefix")
	}
	for _, character := range id {
		if !((character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '-' || character == '_') {
			return errors.New("invalid Jupyter path prefix")
		}
	}
	return nil
}

func validateExecutable(executable string) error {
	if strings.ContainsAny(executable, "\x00\r\n") {
		return errors.New("interpreter contains an invalid character")
	}
	return nil
}

func normalizePathPrefix(pathPrefix string) string {
	return strings.TrimSuffix(pathPrefix, "/") + "/"
}

func serverArguments(directory, pathPrefix string, port int, kernelName string) []string {
	arguments := []string{
		"--ServerApp.ip=127.0.0.1",
		"--ServerApp.port=" + strconv.Itoa(port),
		"--ServerApp.port_retries=0",
		"--ServerApp.certfile=",
		"--ServerApp.keyfile=",
		"--ServerApp.ssl_options={}",
		"--ServerApp.base_url=" + pathPrefix,
		"--ServerApp.root_dir=" + directory,
		"--ServerApp.open_browser=False",
		"--ServerApp.allow_remote_access=False",
		"--ServerApp.quit_button=False",
		"--ServerApp.contents_manager_class=" + contentsManagerClass,
	}
	if kernelName != "" {
		arguments = append(arguments, "--MappingKernelManager.default_kernel_name="+kernelName)
	}
	return arguments
}

func jupyterCommand(serverPython, directory, pathPrefix string, port int, kernelName string) (string, []string, error) {
	if err := validateExecutable(serverPython); err != nil {
		return "", nil, err
	}
	arguments := serverArguments(directory, pathPrefix, port, kernelName)
	if serverPython == "" {
		return "jupyter-lab", arguments, nil
	}
	return serverPython, append([]string{"-m", "jupyterlab"}, arguments...), nil
}

func prepareLocalRuntime(kernelPython, kernelName string) (string, string, error) {
	runtimeDirectory, err := os.MkdirTemp("", "open-server-jupyter-")
	if err != nil {
		return "", "", fmt.Errorf("create temporary Jupyter runtime: %w", err)
	}
	if err := writeContentsManagerModule(runtimeDirectory); err != nil {
		_ = os.RemoveAll(runtimeDirectory)
		return "", "", err
	}
	if kernelPython == "" {
		return runtimeDirectory, "", nil
	}
	kernelPrefix := filepath.Join(runtimeDirectory, "kernel-prefix")
	if err := installKernelSpec(kernelPython, kernelName, kernelPrefix); err != nil {
		_ = os.RemoveAll(runtimeDirectory)
		return "", "", err
	}
	return runtimeDirectory, kernelPrefix, nil
}

func writeContentsManagerModule(directory string) error {
	modulePath := filepath.Join(directory, contentsManagerModule+".py")
	if err := os.WriteFile(modulePath, []byte(contentsManagerPython), 0o600); err != nil {
		return fmt.Errorf("write Jupyter working-directory trash module: %w", err)
	}
	return nil
}

func installKernelSpec(kernelPython, kernelName, prefix string) error {
	if prefix == "" {
		return errors.New("temporary Jupyter kernelspec prefix is empty")
	}
	command := exec.Command(
		kernelPython, "-m", "ipykernel", "install",
		"--prefix", prefix,
		"--name", kernelName,
		"--display-name", "Python (open-server)",
	)
	output, err := command.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message != "" {
			return fmt.Errorf("install temporary Jupyter kernel: %w: %s", err, message)
		}
		return fmt.Errorf("install temporary Jupyter kernel: %w", err)
	}
	return nil
}

func localEnvironment(token, runtimeDirectory, kernelPrefix string) []string {
	environment := append([]string(nil), os.Environ()...)
	environment = setEnvironment(environment, "JUPYTER_TOKEN", token)
	pythonPath := runtimeDirectory
	if inherited := os.Getenv("PYTHONPATH"); inherited != "" {
		pythonPath += string(os.PathListSeparator) + inherited
	}
	environment = setEnvironment(environment, "PYTHONPATH", pythonPath)
	if kernelPrefix != "" {
		dataDirectory := filepath.Join(kernelPrefix, "share", "jupyter")
		jupyterPath := os.Getenv("JUPYTER_PATH")
		if jupyterPath != "" {
			dataDirectory += string(os.PathListSeparator) + jupyterPath
		}
		environment = setEnvironment(environment, "JUPYTER_PATH", dataDirectory)
	}
	return environment
}

func setEnvironment(environment []string, name, value string) []string {
	prefix := name + "="
	filtered := environment[:0]
	for _, item := range environment {
		if !strings.HasPrefix(item, prefix) {
			filtered = append(filtered, item)
		}
	}
	return append(filtered, prefix+value)
}

func remoteCommand(
	rsh, host, serverPython string, localPort int, runDirectory, socketPath string,
) (string, []string, error) {
	if rsh == "" {
		rsh = "ssh"
	}
	if host == "" || strings.HasPrefix(host, "-") || strings.ContainsAny(host, "\x00\r\n") {
		return "", nil, errors.New("invalid SSH host for JupyterLab")
	}
	if err := validateExecutable(serverPython); err != nil {
		return "", nil, err
	}
	if err := validateRemoteRuntime(runDirectory, socketPath); err != nil {
		return "", nil, err
	}
	forward := fmt.Sprintf("127.0.0.1:%d:%s", localPort, socketPath)
	arguments := []string{
		"-T", "-x", "-a",
		"-o", "ExitOnForwardFailure=yes",
		"-o", "SessionType=default",
		"-o", "StdinNull=no",
		"-o", "ForkAfterAuthentication=no",
		"-o", "ControlMaster=no",
		"-o", "ControlPersist=no",
		"-o", "PermitLocalCommand=no",
		"-o", "RemoteCommand=none",
		"-L", forward,
		host,
		remoteJupyterScript(serverPython, runDirectory, socketPath),
	}
	return rsh, arguments, nil
}

func remoteJupyterScript(serverPython, runDirectory, socketPath string) string {
	setup := []string{
		"umask 077",
		"runtime_created=",
		"server_python=" + shellQuote(serverPython),
		"socket_path=" + shellQuote(socketPath),
		`if [ -z "$server_python" ]; then`,
		`  jupyter_entry=$(command -v jupyter-lab) || { echo "open-server: jupyter-lab was not found; pass -py" >&2; exit 1; }`,
		`  IFS= read -r shebang < "$jupyter_entry" || { echo "open-server: cannot read the jupyter-lab launcher; pass -py" >&2; exit 1; }`,
		`  case "$shebang" in`,
		`    '#!'/*) server_python=${shebang#\#!} ;;`,
		`    *) echo "open-server: jupyter-lab does not have an absolute Python shebang; pass -py" >&2; exit 1 ;;`,
		`  esac`,
		`  case "$server_python" in *[![:graph:]]*) echo "open-server: unsupported jupyter-lab shebang; pass -py" >&2; exit 1 ;; esac`,
		"fi",
		`case "$server_python" in -*) echo "open-server: invalid Jupyter Python interpreter" >&2; exit 1 ;; esac`,
		`case "$server_python" in */*) [ -x "$server_python" ] || { echo "open-server: Jupyter Python is not executable" >&2; exit 1; } ;;`,
		`  *) server_python=$(command -v "$server_python") || { echo "open-server: Jupyter Python was not found" >&2; exit 1; } ;;`,
		"esac",
		`"$server_python" -c ` + shellQuote(remoteBootstrapPython) + " " + shellQuote(runDirectory) + " " + shellQuote(socketPath) + " || exit 1",
		"runtime_created=1",
		"JUPYTER_RUNTIME_DIR=" + shellQuote(runDirectory),
		`JUPYTER_CONFIG_PATH=` + shellQuote(runDirectory) + `${JUPYTER_CONFIG_PATH:+:$JUPYTER_CONFIG_PATH}`,
		`PYTHONPATH=` + shellQuote(runDirectory) + `${PYTHONPATH:+:$PYTHONPATH}`,
		"export JUPYTER_RUNTIME_DIR JUPYTER_CONFIG_PATH PYTHONPATH",
		`if [ -d ` + shellQuote(path.Join(runDirectory, "kernel-prefix", "share", "jupyter")) + ` ]; then`,
		`  JUPYTER_PATH=` + shellQuote(path.Join(runDirectory, "kernel-prefix", "share", "jupyter")) + `${JUPYTER_PATH:+:$JUPYTER_PATH}`,
		"  export JUPYTER_PATH",
		"fi",
	}
	serviceCommand := `"$server_python" -m jupyterlab ` +
		`"--ServerApp.sock=$socket_path" "--ServerApp.sock_mode=0600" ` +
		`"--ServerApp.certfile=" "--ServerApp.keyfile=" "--ServerApp.ssl_options={}" ` +
		`"--ServerApp.contents_manager_class=` + contentsManagerClass + `" ` +
		`"--KernelManager.transport=ipc"`
	return supervisedRemoteScript(
		setup,
		serviceCommand,
		[]string{`if [ -n "${runtime_created:-}" ]; then case ` + shellQuote(runDirectory) + ` in /tmp/osj-*) rm -rf -- ` + shellQuote(runDirectory) + ` ;; esac; fi`},
	)
}

func supervisedRemoteScript(setup []string, serviceCommand string, extraCleanup []string) string {
	lines := []string{
		"jupyter_pid=",
		"stdin_watcher_pid=",
		"cleanup() {",
		`  if [ -n "$stdin_watcher_pid" ]; then`,
		`    kill "$stdin_watcher_pid" 2>/dev/null || :`,
		`    wait "$stdin_watcher_pid" 2>/dev/null || :`,
		"  fi",
		`  if [ -n "$jupyter_pid" ]; then`,
		`    kill -TERM -"$jupyter_pid" 2>/dev/null || :`,
		"    sleep 0.5",
		`    kill -KILL -"$jupyter_pid" 2>/dev/null || :`,
		`    wait "$jupyter_pid" 2>/dev/null || :`,
		"  fi",
	}
	for _, command := range extraCleanup {
		lines = append(lines, "  "+command)
	}
	lines = append(lines,
		"}",
		"trap cleanup EXIT",
		"trap 'exit 143' HUP INT TERM",
	)
	lines = append(lines, setup...)
	lines = append(lines,
		"exec 3<&0",
		"setsid "+serviceCommand+" </dev/null 3<&- &",
		"jupyter_pid=$!",
		`(cat <&3 >/dev/null; kill -TERM "$$" 2>/dev/null || :) &`,
		"stdin_watcher_pid=$!",
		`wait "$jupyter_pid"`,
		"status=$?",
		`kill "$stdin_watcher_pid" 2>/dev/null || :`,
		`wait "$stdin_watcher_pid" 2>/dev/null || :`,
		"stdin_watcher_pid=",
		`exit "$status"`,
	)
	return strings.Join(lines, "\n")
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func newRemoteRuntime() (string, string, error) {
	nonce, err := randomHex(remoteNonceBytes)
	if err != nil {
		return "", "", fmt.Errorf("choose remote Jupyter runtime: %w", err)
	}
	runDirectory := remoteRunPrefix + nonce
	return runDirectory, path.Join(runDirectory, "j.sock"), nil
}

func validateRemoteRuntime(runDirectory, socketPath string) error {
	if !strings.HasPrefix(runDirectory, remoteRunPrefix) ||
		len(runDirectory) != len(remoteRunPrefix)+remoteNonceBytes*2 {
		return errors.New("invalid remote Jupyter runtime directory")
	}
	if _, err := hex.DecodeString(strings.TrimPrefix(runDirectory, remoteRunPrefix)); err != nil {
		return errors.New("invalid remote Jupyter runtime directory")
	}
	if socketPath != path.Join(runDirectory, "j.sock") {
		return errors.New("invalid remote Jupyter socket path")
	}
	return nil
}

func remoteConfigFrame(configuration remoteLaunchConfig) ([]byte, error) {
	payload, err := json.Marshal(configuration)
	if err != nil {
		return nil, fmt.Errorf("encode remote Jupyter configuration: %w", err)
	}
	encoded := base64.StdEncoding.EncodeToString(payload)
	return []byte(fmt.Sprintf("OSJ1 %d %s\n", len(payload), encoded)), nil
}

const contentsManagerPython = `
"""Jupyter contents manager that keeps trash in the launched working directory."""

import errno
import os
import shutil
import stat
from datetime import datetime
from urllib.parse import quote_from_bytes

from anyio.to_thread import run_sync
from jupyter_core.paths import is_hidden
from jupyter_server.services.contents.largefilemanager import AsyncLargeFileManager
from tornado import web


def _private_directory(directory):
    """Create or validate a private, user-owned trash directory."""
    try:
        os.mkdir(directory, 0o700)
    except FileExistsError:
        pass
    details = os.lstat(directory)
    current_user = getattr(os, "geteuid", lambda: details.st_uid)()
    if (
        not stat.S_ISDIR(details.st_mode)
        or os.path.islink(directory)
        or details.st_uid != current_user
    ):
        raise OSError(errno.EACCES, "unsafe working-directory trash path", directory)
    if os.name != "nt" and stat.S_IMODE(details.st_mode) != 0o700:
        os.chmod(directory, 0o700)
        if stat.S_IMODE(os.lstat(directory).st_mode) != 0o700:
            raise OSError(errno.EACCES, "trash directory is not private", directory)


def _trash_paths(root_directory):
    """Return private Freedesktop-style trash paths inside the Jupyter root."""
    user_id = getattr(os, "getuid", lambda: 0)()
    trash = os.path.join(os.path.realpath(root_directory), f".Trash-{user_id}")
    files = os.path.join(trash, "files")
    info = os.path.join(trash, "info")
    for directory in (trash, files, info):
        _private_directory(directory)
    return trash, files, info


def _reserve_destination(source, files_directory, info_directory):
    """Reserve a collision-free trash name by exclusively creating its metadata."""
    original_name = os.path.basename(source)
    stem, extension = os.path.splitext(original_name)
    counter = 0
    while True:
        name = original_name if counter == 0 else f"{stem} {counter}{extension}"
        destination = os.path.join(files_directory, name)
        information = os.path.join(info_directory, name + ".trashinfo")
        counter += 1
        if os.path.lexists(destination):
            continue
        flags = os.O_WRONLY | os.O_CREAT | os.O_EXCL
        if hasattr(os, "O_NOFOLLOW"):
            flags |= os.O_NOFOLLOW
        try:
            descriptor = os.open(information, flags, 0o600)
        except FileExistsError:
            continue
        return destination, information, descriptor


def _move_to_working_trash(source, root_directory):
    """Move one file or directory into the Jupyter root's persistent trash."""
    user_id = getattr(os, "getuid", lambda: 0)()
    trash = os.path.join(os.path.realpath(root_directory), f".Trash-{user_id}")
    source_location = os.path.join(
        os.path.realpath(os.path.dirname(source)), os.path.basename(source)
    )
    try:
        source_is_trash = os.path.commonpath((source_location, trash)) == trash
    except ValueError:
        source_is_trash = False
    if source_is_trash:
        raise OSError(errno.EINVAL, "cannot trash the working-directory trash", source)

    trash, files_directory, info_directory = _trash_paths(root_directory)
    destination, information, descriptor = _reserve_destination(
        source, files_directory, info_directory
    )
    encoded_source = quote_from_bytes(os.fsencode(os.path.abspath(source)), safe="/")
    metadata = (
        "[Trash Info]\n"
        f"Path={encoded_source}\n"
        f"DeletionDate={datetime.now().strftime('%Y-%m-%dT%H:%M:%S')}\n"
    )
    try:
        with os.fdopen(descriptor, "w", encoding="utf-8") as handle:
            handle.write(metadata)
        try:
            os.rename(source, destination)
        except OSError as error:
            if error.errno != errno.EXDEV:
                raise
            shutil.move(source, destination)
    except BaseException:
        try:
            os.unlink(information)
        except OSError:
            pass
        raise


class WorkingDirectoryContentsManager(AsyncLargeFileManager):
    """Store Jupyter-deleted files under the launched working directory."""

    async def delete_file(self, path):
        if not self.delete_to_trash:
            return await super().delete_file(path)

        path = path.strip("/")
        os_path = self._get_os_path(path)
        if not self.allow_hidden and is_hidden(os_path, self.root_dir):
            raise web.HTTPError(400, f"Cannot delete file or directory {os_path!r}")
        if not os.path.exists(os_path):
            raise web.HTTPError(404, f"File or directory does not exist: {os_path}")
        if not self.is_writable(path):
            raise web.HTTPError(403, f"Permission denied: {path}") from None

        self.log.debug("Sending %s to working-directory trash", os_path)
        try:
            await run_sync(_move_to_working_trash, os_path, self.root_dir)
        except OSError as error:
            raise web.HTTPError(
                400, f"working-directory trash failed: {error}"
            ) from error
`

const remoteBootstrapPython = `
import base64
import json
import os
import re
import shutil
import stat
import subprocess
import sys


def fail(message):
    """Stop the remote bootstrap with a concise diagnostic."""
    raise SystemExit(f"open-server: {message}")


def read_configuration():
    """Read and validate one length-framed base64 JSON configuration."""
    line = sys.stdin.buffer.readline(1_048_577)
    if not line.endswith(b"\n") or len(line) > 1_048_576:
        fail("invalid Jupyter configuration frame")
    fields = line[:-1].split(b" ", 2)
    if len(fields) != 3 or fields[0] != b"OSJ1":
        fail("invalid Jupyter configuration frame")
    try:
        expected_length = int(fields[1])
        payload = base64.b64decode(fields[2], validate=True)
        configuration = json.loads(payload)
    except (ValueError, TypeError, json.JSONDecodeError) as error:
        fail(f"invalid Jupyter configuration frame: {error}")
    if expected_length != len(payload) or not isinstance(configuration, dict):
        fail("invalid Jupyter configuration frame length")
    return configuration


def check_features():
    """Verify fail-closed Unix-socket and IPC support in this interpreter."""
    try:
        import jupyterlab  # noqa: F401
        import zmq
        from jupyter_client.manager import KernelManager
        from jupyter_server.auth.identity import IdentityProvider
        from jupyter_server.serverapp import ServerApp
        from jupyter_server.services.contents.largefilemanager import AsyncLargeFileManager  # noqa: F401
    except Exception as error:
        fail(f"JupyterLab security feature probe failed: {error}")
    server_traits = ServerApp.class_traits()
    if "sock" not in server_traits or "sock_mode" not in server_traits:
        fail("this JupyterLab does not support private Unix sockets")
    if "token" not in IdentityProvider.class_traits():
        fail("this JupyterLab does not support IdentityProvider token configuration")
    transport = KernelManager.class_traits().get("transport")
    if transport is None or "ipc" not in getattr(transport, "values", ()):
        fail("this JupyterLab does not support IPC kernel transport")
    curve = False
    try:
        from jupyter_server.services.kernels.kernelmanager import MappingKernelManager

        curve = (
            "transport_encryption" in MappingKernelManager.class_traits()
            and "transport_encryption" in KernelManager.class_traits()
            and bool(zmq.has("curve"))
        )
    except Exception:
        curve = False
    return curve


def create_runtime(configuration, expected_run_directory, expected_socket_path):
    """Create and verify the private short runtime directory."""
    run_directory = configuration.get("run_directory")
    socket_path = configuration.get("socket_path")
    if run_directory != expected_run_directory or socket_path != expected_socket_path:
        fail("remote Jupyter runtime does not match its SSH forwarding lease")
    name = os.path.basename(run_directory)
    if os.path.dirname(run_directory) != "/tmp" or not re.fullmatch(r"osj-[0-9a-f]{32}", name):
        fail("invalid remote Jupyter runtime path")
    sample_ipc_path = os.path.join(run_directory, "kernel-" + "0" * 36 + "-ipc-65535")
    if max(len(os.fsencode(socket_path)), len(os.fsencode(sample_ipc_path))) > 100:
        fail("remote Jupyter Unix-socket path is too long")
    created = False
    try:
        try:
            os.mkdir(run_directory, 0o700)
            created = True
        except FileExistsError:
            fail("remote Jupyter runtime already exists")
        details = os.lstat(run_directory)
        if (
            not stat.S_ISDIR(details.st_mode)
            or details.st_uid != os.geteuid()
            or stat.S_IMODE(details.st_mode) != 0o700
        ):
            fail("remote Jupyter runtime is not a private user-owned directory")
    except BaseException:
        if created:
            try:
                os.rmdir(run_directory)
            except OSError:
                pass
        raise


def install_kernel(configuration, run_directory):
    """Install the requested temporary kernelspec inside the private runtime."""
    kernel_python = configuration.get("kernel_python")
    kernel_name = configuration.get("kernel_name")
    if not isinstance(kernel_python, str) or not isinstance(kernel_name, str):
        fail("invalid temporary Jupyter kernel configuration")
    if not kernel_python:
        if kernel_name:
            fail("temporary Jupyter kernel name has no interpreter")
        return
    if not kernel_name:
        fail("temporary Jupyter kernel interpreter has no name")
    prefix = os.path.join(run_directory, "kernel-prefix")
    result = subprocess.run(
        [
            kernel_python,
            "-m",
            "ipykernel",
            "install",
            "--prefix",
            prefix,
            "--name",
            kernel_name,
            "--display-name",
            "Python (open-server)",
        ],
        check=False,
    )
    if result.returncode != 0:
        fail("temporary Jupyter kernelspec installation failed")


def install_contents_manager(configuration, run_directory):
    """Write the bundled working-directory trash contents manager privately."""
    source = configuration.get("contents_manager")
    if not isinstance(source, str) or not source:
        fail("invalid Jupyter contents manager")
    module_path = os.path.join(run_directory, "open_server_jupyter.py")
    flags = os.O_WRONLY | os.O_CREAT | os.O_EXCL
    if hasattr(os, "O_NOFOLLOW"):
        flags |= os.O_NOFOLLOW
    descriptor = os.open(module_path, flags, 0o600)
    with os.fdopen(descriptor, "w", encoding="utf-8") as handle:
        handle.write(source)
    if stat.S_IMODE(os.stat(module_path).st_mode) != 0o600:
        fail("Jupyter contents manager is not private")


def write_server_config(configuration, run_directory, curve_enabled):
    """Write the private Jupyter Server JSON configuration."""
    required = ("directory", "path_prefix", "token")
    if any(not isinstance(configuration.get(key), str) for key in required):
        fail("invalid Jupyter server configuration")
    if not configuration["directory"] or not configuration["token"]:
        fail("incomplete Jupyter server configuration")
    kernel_config = {"transport": "ipc"}
    if curve_enabled:
        kernel_config["transport_encryption"] = "auto"
    server_config = {
        "IdentityProvider": {"token": configuration["token"]},
        "KernelManager": kernel_config,
        "ServerApp": {
            "allow_remote_access": False,
            "base_url": configuration["path_prefix"],
            "open_browser": False,
            "quit_button": False,
            "root_dir": configuration["directory"],
        },
    }
    mapping_kernel_config = {}
    if curve_enabled:
        mapping_kernel_config["transport_encryption"] = "auto"
    if configuration["kernel_name"]:
        mapping_kernel_config["default_kernel_name"] = configuration["kernel_name"]
    if mapping_kernel_config:
        server_config["MappingKernelManager"] = mapping_kernel_config
    config_path = os.path.join(run_directory, "jupyter_server_config.json")
    flags = os.O_WRONLY | os.O_CREAT | os.O_EXCL
    if hasattr(os, "O_NOFOLLOW"):
        flags |= os.O_NOFOLLOW
    descriptor = os.open(config_path, flags, 0o600)
    with os.fdopen(descriptor, "w", encoding="utf-8") as handle:
        json.dump(server_config, handle)
        handle.write("\n")
    if stat.S_IMODE(os.stat(config_path).st_mode) != 0o600:
        fail("remote Jupyter configuration is not private")


def main():
    """Prepare a fail-closed private remote Jupyter launch."""
    if len(sys.argv) != 3:
        fail("invalid Jupyter bootstrap arguments")
    os.umask(0o077)
    configuration = read_configuration()
    curve_enabled = check_features()
    create_runtime(configuration, sys.argv[1], sys.argv[2])
    try:
        install_contents_manager(configuration, sys.argv[1])
        install_kernel(configuration, sys.argv[1])
        write_server_config(configuration, sys.argv[1], curve_enabled)
    except BaseException:
        shutil.rmtree(sys.argv[1], ignore_errors=True)
        raise


if __name__ == "__main__":
    main()
`

func waitUntilReady(instance *runningInstance, diagnostic *captureWriter) error {
	statusURL := instance.target.String() + instance.pathPrefix + "api/status"
	client := &http.Client{Timeout: 500 * time.Millisecond}
	deadline := time.NewTimer(readinessTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		request, _ := http.NewRequest(http.MethodGet, statusURL, nil)
		request.Header.Set("Authorization", "token "+instance.token)
		response, err := client.Do(request)
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode >= 200 && response.StatusCode < 300 {
				return nil
			}
		}
		select {
		case <-instance.process.done:
			message := strings.TrimSpace(diagnostic.String())
			if message != "" {
				return fmt.Errorf("JupyterLab exited before becoming ready: %s", message)
			}
			return errors.New("JupyterLab exited before becoming ready")
		case <-deadline.C:
			return errors.New("JupyterLab did not become ready within 30 seconds")
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

func pickLocalPort() (int, error) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("reserve local Jupyter port: %w", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		return 0, fmt.Errorf("release local Jupyter port: %w", err)
	}
	return port, nil
}

func randomHex(size int) (string, error) {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

type runningInstance struct {
	process         *runningProcess
	target          *url.URL
	token           string
	pathPrefix      string
	temporaryPrefix string
	stopOnce        sync.Once
}

func (i *runningInstance) stop() {
	i.stopOnce.Do(func() {
		shutdownKernels(i.target, i.pathPrefix, i.token)
		i.process.stop()
		_ = os.RemoveAll(i.temporaryPrefix)
	})
}

func (i *runningInstance) stopWithoutAPI() {
	i.stopOnce.Do(func() {
		i.process.stop()
		_ = os.RemoveAll(i.temporaryPrefix)
	})
}

type kernelRecord struct {
	ID string `json:"id"`
}

func shutdownKernels(target *url.URL, pathPrefix, token string) {
	ctx, cancel := context.WithTimeout(context.Background(), kernelRequestTimeout)
	defer cancel()
	kernels, err := listKernels(ctx, target, pathPrefix, token)
	if err != nil {
		return
	}
	client := &http.Client{}
	for _, kernel := range kernels {
		if kernel.ID == "" {
			continue
		}
		endpoint := target.String() + pathPrefix + "api/kernels/" + url.PathEscape(kernel.ID)
		request, requestErr := http.NewRequestWithContext(ctx, http.MethodDelete, endpoint, nil)
		if requestErr != nil {
			continue
		}
		request.Header.Set("Authorization", "token "+token)
		response, requestErr := client.Do(request)
		if requestErr == nil {
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
		}
	}
	deadline := time.Now().Add(kernelGracePeriod)
	for time.Now().Before(deadline) {
		remaining, listErr := listKernels(ctx, target, pathPrefix, token)
		if listErr != nil || len(remaining) == 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func listKernels(ctx context.Context, target *url.URL, pathPrefix, token string) ([]kernelRecord, error) {
	endpoint := target.String() + pathPrefix + "api/kernels"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "token "+token)
	response, err := (&http.Client{}).Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("list Jupyter kernels: %s", response.Status)
	}
	var kernels []kernelRecord
	decoder := json.NewDecoder(io.LimitReader(response.Body, 1<<20))
	if err := decoder.Decode(&kernels); err != nil {
		return nil, err
	}
	return kernels, nil
}

type runningProcess struct {
	command  *exec.Cmd
	stdin    io.Closer
	done     chan struct{}
	stopOnce sync.Once
}

func newRunningProcess(command *exec.Cmd, stdin io.Closer, onDone func()) *runningProcess {
	process := &runningProcess{command: command, stdin: stdin, done: make(chan struct{})}
	go func() {
		_ = command.Wait()
		if process.stdin != nil {
			_ = process.stdin.Close()
		}
		if onDone != nil {
			onDone()
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
			case <-time.After(processGracePeriod):
			}
		}
		_ = terminateCommand(p.command)
		select {
		case <-p.done:
		case <-time.After(processGracePeriod):
			_ = killCommand(p.command)
			<-p.done
		}
	})
}

type captureWriter struct {
	mu      sync.Mutex
	buffer  bytes.Buffer
	output  io.Writer
	secret  []byte
	pending []byte
}

type remoteSocketDiagnosticFilter struct {
	mu      sync.Mutex
	output  io.Writer
	pending []byte
}

func newRemoteSocketDiagnosticFilter(output io.Writer) *remoteSocketDiagnosticFilter {
	return &remoteSocketDiagnosticFilter{output: output}
}

func (w *remoteSocketDiagnosticFilter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pending = append(w.pending, p...)
	for {
		newline := bytes.IndexByte(w.pending, '\n')
		if newline < 0 {
			break
		}
		line := w.pending[:newline+1]
		if !isMissingRemoteSocketDiagnostic(line) {
			_, _ = w.output.Write(line)
		}
		w.pending = w.pending[newline+1:]
	}
	if len(w.pending) > 128 {
		_, _ = w.output.Write(w.pending)
		w.pending = nil
	}
	return len(p), nil
}

func (w *remoteSocketDiagnosticFilter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.pending) != 0 {
		_, _ = w.output.Write(w.pending)
		w.pending = nil
	}
}

func isMissingRemoteSocketDiagnostic(line []byte) bool {
	message := strings.TrimSuffix(strings.TrimSuffix(string(line), "\n"), "\r")
	const prefix = "channel "
	const suffix = ": open failed: connect failed: open failed"
	if !strings.HasPrefix(message, prefix) || !strings.HasSuffix(message, suffix) {
		return false
	}
	channel := strings.TrimSuffix(strings.TrimPrefix(message, prefix), suffix)
	if channel == "" {
		return false
	}
	for _, character := range channel {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func newCaptureWriter(output io.Writer, secret string) *captureWriter {
	return &captureWriter{output: output, secret: []byte(secret)}
}

func (w *captureWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
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
