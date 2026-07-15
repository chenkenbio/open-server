package tensorboard

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Instance struct {
	Target  *url.URL
	Handler http.Handler
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
	remotePort := localPort
	if l.remote {
		remotePort, err = randomRemotePort()
		if err != nil {
			return nil, err
		}
	}

	var command *exec.Cmd
	if l.remote {
		executable, arguments, err := remoteCommand(l.rsh, l.host, l.python, logDirectory, pathPrefix, localPort, remotePort)
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
	configureCommand(command)
	diagnostic := &captureWriter{output: l.output}
	command.Stdout = diagnostic
	command.Stderr = diagnostic
	if err := command.Start(); err != nil {
		return nil, fmt.Errorf("start TensorBoard: %w", err)
	}
	process := newRunningProcess(command)
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		process.stop()
		return nil, errors.New("TensorBoard launcher is closed")
	}
	l.processes = append(l.processes, process)
	l.mu.Unlock()

	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(localPort))
	target, _ := url.Parse("http://" + address)
	probeURL := target.String() + pathPrefix + "/"
	probeClient := &http.Client{Timeout: 500 * time.Millisecond}
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		response, probeErr := probeClient.Get(probeURL)
		if probeErr == nil {
			_ = response.Body.Close()
			return &Instance{Target: target}, nil
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
	if strings.ContainsAny(pythonInterpreter, "\x00\r\n") {
		return "", nil, errors.New("invalid Python interpreter")
	}
	return pythonInterpreter, append([]string{"-m", "tensorboard.main"}, arguments...), nil
}

func remoteCommand(rsh, host, pythonInterpreter, logDirectory, pathPrefix string, localPort, remotePort int) (string, []string, error) {
	if rsh == "" {
		rsh = "ssh"
	}
	if host == "" || strings.HasPrefix(host, "-") || strings.ContainsAny(host, "\x00\r\n") {
		return "", nil, errors.New("invalid SSH host for TensorBoard")
	}
	forward := fmt.Sprintf("127.0.0.1:%d:127.0.0.1:%d", localPort, remotePort)
	tensorBoardExecutable, remoteArguments, err := tensorBoardCommand(pythonInterpreter, logDirectory, pathPrefix, remotePort)
	if err != nil {
		return "", nil, err
	}
	quoted := make([]string, 0, len(remoteArguments)+2)
	quoted = append(quoted, "exec", shellQuote(tensorBoardExecutable))
	for _, argument := range remoteArguments {
		quoted = append(quoted, shellQuote(argument))
	}
	arguments := []string{
		"-q", "-T", "-x", "-a",
		"-o", "ExitOnForwardFailure=yes",
		"-o", "ForkAfterAuthentication=no",
		"-o", "ControlMaster=no",
		"-o", "ControlPersist=no",
		"-o", "PermitLocalCommand=no",
		"-o", "RemoteCommand=none",
		"-o", "StdinNull=yes",
		"-L", forward,
		host,
		strings.Join(quoted, " "),
	}
	return rsh, arguments, nil
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

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

func randomRemotePort() (int, error) {
	var random [2]byte
	if _, err := rand.Read(random[:]); err != nil {
		return 0, fmt.Errorf("choose remote TensorBoard port: %w", err)
	}
	return 49152 + int(binary.BigEndian.Uint16(random[:]))%(65535-49152+1), nil
}

type runningProcess struct {
	command  *exec.Cmd
	done     chan struct{}
	stopOnce sync.Once
}

func newRunningProcess(command *exec.Cmd) *runningProcess {
	process := &runningProcess{command: command, done: make(chan struct{})}
	go func() {
		_ = command.Wait()
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
		_ = terminateCommand(p.command)
		select {
		case <-p.done:
		case <-time.After(3 * time.Second):
			_ = killCommand(p.command)
			<-p.done
		}
	})
}

type captureWriter struct {
	mu     sync.Mutex
	buffer bytes.Buffer
	output io.Writer
}

func (w *captureWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
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
	return len(p), nil
}

func (w *captureWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buffer.String()
}
