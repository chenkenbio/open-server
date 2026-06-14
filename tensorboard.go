package main

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"syscall"
	"time"
)

// tbPathPrefix is the URL prefix under which TensorBoard is mounted. TensorBoard
// is launched with --path_prefix set to this value so that every asset and API
// URL it emits is already rooted here; the reverse proxy can then forward the
// request path unchanged (no StripPrefix needed).
const tbPathPrefix = "/tensorboard"

// tbProcess holds a running TensorBoard subprocess and the reverse proxy that
// forwards requests to it.
type tbProcess struct {
	cmd      *exec.Cmd
	proxy    http.Handler
	port     int
	stopOnce sync.Once
}

// pickFreeLocalPort asks the OS for an unused loopback TCP port by binding to
// port 0 and immediately releasing it. There is a small race between releasing
// the port here and TensorBoard binding it, but that is acceptable for a
// single-user personal tool.
func pickFreeLocalPort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("could not reserve a local port for tensorboard: %w", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if err := ln.Close(); err != nil {
		return 0, fmt.Errorf("could not release reserved port %d: %w", port, err)
	}
	return port, nil
}

// startTensorboard resolves the tensorboard binary, validates the log directory,
// launches tensorboard bound to a free loopback port under tbPathPrefix, and
// builds a reverse proxy pointed at it. The returned tbProcess must be stopped
// by the caller (see stop) to avoid orphaning the subprocess.
func startTensorboard(tbBin, tbDir string) (*tbProcess, error) {
	bin := tbBin
	if bin == "" {
		bin = "tensorboard"
	}
	binPath, err := exec.LookPath(bin)
	if err != nil {
		return nil, fmt.Errorf("tensorboard not found (%q); install it or pass --tb-bin: %w", bin, err)
	}

	logDir := expandHomePath(tbDir)
	info, err := os.Stat(logDir)
	if err != nil {
		return nil, fmt.Errorf("cannot access tensorboard logdir %q: %w", tbDir, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("tensorboard logdir %q is not a directory", tbDir)
	}

	port, err := pickFreeLocalPort()
	if err != nil {
		return nil, err
	}

	cmd := exec.Command(binPath,
		"--logdir", logDir,
		"--host", "127.0.0.1",
		"--port", strconv.Itoa(port),
		"--path_prefix", tbPathPrefix,
	)
	// Own process group so the whole TensorBoard process tree can be signaled at
	// once on shutdown (see stop).
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	logWriter := &prefixWriter{prefix: "[tensorboard] "}
	cmd.Stdout = logWriter
	cmd.Stderr = logWriter
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start tensorboard: %w", err)
	}

	target, err := url.Parse("http://127.0.0.1:" + strconv.Itoa(port))
	if err != nil {
		// Should be unreachable; clean up the child we just started.
		_ = cmd.Process.Kill()
		return nil, fmt.Errorf("invalid tensorboard target url: %w", err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	defaultDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		defaultDirector(req)
		// SingleHostReverseProxy leaves req.Host as the inbound host; force it to
		// the backend so TensorBoard sees a Host it expects.
		req.Host = target.Host
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		log.Printf("tensorboard proxy error for %s: %v", r.URL.Path, err)
		http.Error(w, "TensorBoard is starting up or unavailable; retry shortly.", http.StatusBadGateway)
	}

	return &tbProcess{cmd: cmd, proxy: proxy, port: port}, nil
}

// waitReady polls the TensorBoard port until it accepts a connection or the
// timeout elapses, returning whether it became ready. It lets callers delay
// printing the access link until TensorBoard is actually serving.
func (p *tbProcess) waitReady(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(p.port))
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return true
		}
		time.Sleep(250 * time.Millisecond)
	}
	return false
}

// stop terminates the TensorBoard process tree. It first sends SIGTERM to the
// whole process group (negative PID), waits briefly for a graceful exit, then
// force-kills if still alive. It is safe to call multiple times.
func (p *tbProcess) stop() {
	p.stopOnce.Do(func() {
		if p.cmd == nil || p.cmd.Process == nil {
			return
		}
		pid := p.cmd.Process.Pid
		// Negative PID targets the entire process group created via Setpgid.
		if err := syscall.Kill(-pid, syscall.SIGTERM); err != nil {
			// Fall back to signaling just the direct child.
			_ = p.cmd.Process.Signal(syscall.SIGTERM)
		}

		done := make(chan struct{})
		go func() {
			_, _ = p.cmd.Process.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil {
				_ = p.cmd.Process.Kill()
			}
		}
		log.Printf("tensorboard stopped")
	})
}

// prefixWriter prefixes each write with a fixed tag so TensorBoard's output is
// visible in the server logs but clearly distinguished from open-server's own.
type prefixWriter struct {
	prefix string
}

func (w *prefixWriter) Write(p []byte) (int, error) {
	log.Printf("%s%s", w.prefix, string(p))
	return len(p), nil
}
