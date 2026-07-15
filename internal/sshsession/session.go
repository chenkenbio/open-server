package sshsession

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"
)

type Options struct {
	Executable string
	Host       string
	Stderr     io.Writer
}

type Session struct {
	Client      *sftp.Client
	cmd         *exec.Cmd
	stdin       io.WriteCloser
	done        chan struct{}
	processDone chan struct{}

	errMu     sync.Mutex
	err       error
	closeOnce sync.Once
	doneOnce  sync.Once
}

func Command(executable, host string) (string, []string, error) {
	if executable == "" {
		executable = "ssh"
	}
	if host == "" || strings.HasPrefix(host, "-") || strings.ContainsAny(host, "\x00\r\n") {
		return "", nil, errors.New("invalid SSH host")
	}
	// SessionType requests the standard subsystem while overriding aliases that
	// set SessionType none. The other options disable unrelated channels while
	// preserving normal ssh_config, keys, agents, host checking, aliases, and
	// ProxyJump behavior.
	args := []string{
		"-T", "-x", "-a",
		"-o", "ClearAllForwardings=yes",
		"-o", "ForkAfterAuthentication=no",
		"-o", "ControlMaster=no",
		"-o", "ControlPersist=no",
		"-o", "PermitLocalCommand=no",
		"-o", "RemoteCommand=none",
		"-o", "StdinNull=no",
		"-o", "SessionType=subsystem",
		host, "sftp",
	}
	return executable, args, nil
}

func Start(ctx context.Context, options Options) (*Session, error) {
	executable, args, err := Command(options.Executable, options.Host)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, executable, args...)
	configureProcess(cmd)
	cmd.Cancel = func() error { return killProcess(cmd) }
	cmd.WaitDelay = 2 * time.Second
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("prepare SSH input: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("prepare SSH output: %w", err)
	}
	var diagnostic limitedBuffer
	stderr := options.Stderr
	if stderr == nil {
		stderr = os.Stderr
	}
	cmd.Stderr = io.MultiWriter(stderr, &diagnostic)
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("start SSH: %w", err)
	}

	client, err := sftp.NewClientPipe(stdout, stdin, sftp.UseConcurrentReads(true), sftp.UseConcurrentWrites(true))
	if err != nil {
		_ = stdin.Close()
		if cmd.Process != nil {
			_ = killProcess(cmd)
		}
		_ = cmd.Wait()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, classifyStartError(diagnostic.String())
	}
	session := &Session{Client: client, cmd: cmd, stdin: stdin, done: make(chan struct{}), processDone: make(chan struct{})}
	go func() {
		err := cmd.Wait()
		close(session.processDone)
		session.markDone(err)
	}()
	go func() { session.markDone(client.Wait()) }()
	return session, nil
}

func (s *Session) markDone(err error) {
	s.doneOnce.Do(func() {
		s.errMu.Lock()
		s.err = err
		s.errMu.Unlock()
		close(s.done)
	})
}

func (s *Session) Done() <-chan struct{} {
	return s.done
}

func (s *Session) Err() error {
	select {
	case <-s.done:
		s.errMu.Lock()
		defer s.errMu.Unlock()
		return s.err
	default:
		return nil
	}
}

func (s *Session) Close() error {
	s.closeOnce.Do(func() {
		_ = s.stdin.Close()
		clientClosed := make(chan struct{})
		go func() {
			_ = s.Client.Close()
			close(clientClosed)
		}()
		select {
		case <-s.processDone:
		case <-time.After(200 * time.Millisecond):
		}
		select {
		case <-s.processDone:
		default:
			if s.cmd.Process != nil {
				_ = killProcess(s.cmd)
			}
		}
		select {
		case <-s.processDone:
		case <-time.After(2 * time.Second):
			if s.cmd.Process != nil {
				_ = killProcess(s.cmd)
			}
			<-s.processDone
		}
		select {
		case <-clientClosed:
		case <-time.After(time.Second):
		}
	})
	return s.Err()
}

func classifyStartError(stderr string) error {
	lower := strings.ToLower(stderr)
	switch {
	case strings.Contains(lower, "host key verification failed"), strings.Contains(lower, "remote host identification has changed"):
		return errors.New("SSH host-key verification failed")
	case strings.Contains(lower, "permission denied"), strings.Contains(lower, "authentication failed"):
		return errors.New("SSH authentication failed")
	case strings.Contains(lower, "subsystem request failed"), strings.Contains(lower, "unknown subsystem"), strings.Contains(lower, "subsystem not found"):
		return errors.New("the remote SSH server does not provide an SFTP subsystem")
	case strings.Contains(lower, "subsystem was not found"):
		return errors.New("the remote SSH server does not provide an SFTP subsystem")
	default:
		return errors.New("could not initialize an SFTP session over SSH")
	}
}

type limitedBuffer struct {
	buf bytes.Buffer
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	const limit = 32 << 10
	originalLength := len(p)
	if len(p) >= limit {
		b.buf.Reset()
		_, _ = b.buf.Write(p[len(p)-limit:])
		return originalLength, nil
	}
	if b.buf.Len()+len(p) > limit {
		data := append([]byte(nil), b.buf.Bytes()[b.buf.Len()+len(p)-limit:]...)
		b.buf.Reset()
		_, _ = b.buf.Write(data)
	}
	_, _ = b.buf.Write(p)
	return originalLength, nil
}

func (b *limitedBuffer) String() string { return b.buf.String() }
