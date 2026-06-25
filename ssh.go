package main

import (
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	cleanupWatcherCommand = "__open_server_cleanup_watcher"
	socketPIDFile         = "pid"
	socketRunDirPrefix    = "run-"
)

type sshSocketTarget struct {
	SocketPath   string
	RunDir       string
	RemoveRunDir bool
}

func defaultSSHHost() string {
	host, hostErr := os.Hostname()
	user := os.Getenv("USER")
	if user == "" {
		user = os.Getenv("LOGNAME")
	}
	if user == "" || hostErr != nil || host == "" {
		return host
	}
	return user + "@" + host
}

func validateTransportFlags(useSSH bool, explicitFlags map[string]bool) error {
	if !useSSH {
		return nil
	}
	if explicitFlags["address"] || explicitFlags["a"] {
		return errors.New("--address/-a cannot be used with --ssh; the server will not bind a TCP address")
	}
	if explicitFlags["port"] || explicitFlags["p"] {
		return errors.New("--port/-p cannot be used with --ssh; use --local-port for the browser-side tunnel port")
	}
	return nil
}

func defaultSocketBaseDir() string {
	base := os.Getenv("TMPDIR")
	if base == "" {
		base = "/tmp"
	}
	return filepath.Join(base, fmt.Sprintf("open-server-%d", os.Getuid()))
}

func fileUID(info os.FileInfo) (uint32, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, errors.New("could not inspect file owner")
	}
	return stat.Uid, nil
}

func validatePrivateDir(dir string) error {
	info, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%q is not a directory", dir)
	}
	uid, err := fileUID(info)
	if err != nil {
		return err
	}
	if uid != uint32(os.Getuid()) {
		return fmt.Errorf("%q is not owned by the current user", dir)
	}
	perm := info.Mode().Perm()
	if perm&0o077 != 0 || perm&0o700 != 0o700 {
		return fmt.Errorf("%q must have mode 0700; got %04o", dir, perm)
	}
	return nil
}

func ensurePrivateDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	info, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%q is not a directory", dir)
	}
	uid, err := fileUID(info)
	if err != nil {
		return err
	}
	if uid != uint32(os.Getuid()) {
		return fmt.Errorf("%q is not owned by the current user", dir)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	return validatePrivateDir(dir)
}

func readRunPID(runDir string) (int, error) {
	data, err := os.ReadFile(filepath.Join(runDir, socketPIDFile))
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, err
	}
	return pid, nil
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func cleanupStaleSocketDirs(baseDir string) error {
	entries, err := os.ReadDir(baseDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), socketRunDirPrefix) {
			continue
		}
		runDir := filepath.Join(baseDir, entry.Name())
		pid, err := readRunPID(runDir)
		if err != nil || !processAlive(pid) {
			if removeErr := os.RemoveAll(runDir); removeErr != nil {
				return removeErr
			}
		}
	}
	return nil
}

func removeExistingSocket(socketPath string) error {
	info, err := os.Lstat(socketPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("refusing to replace non-socket path %q", socketPath)
	}
	return os.Remove(socketPath)
}

func prepareSSHSocketTarget(socketOverride string) (*sshSocketTarget, error) {
	if socketOverride != "" {
		socketPath, err := filepath.Abs(expandHomePath(socketOverride))
		if err != nil {
			return nil, fmt.Errorf("cannot resolve socket path %q: %w", socketOverride, err)
		}
		if err := validatePrivateDir(filepath.Dir(socketPath)); err != nil {
			return nil, fmt.Errorf("unsafe --socket parent: %w", err)
		}
		if err := removeExistingSocket(socketPath); err != nil {
			return nil, err
		}
		return &sshSocketTarget{SocketPath: socketPath, RunDir: filepath.Dir(socketPath)}, nil
	}

	baseDir := defaultSocketBaseDir()
	if err := ensurePrivateDir(baseDir); err != nil {
		return nil, fmt.Errorf("cannot prepare socket base %q: %w", baseDir, err)
	}
	if err := cleanupStaleSocketDirs(baseDir); err != nil {
		return nil, fmt.Errorf("cannot clean stale socket dirs: %w", err)
	}
	suffix, err := generateRandomToken(8)
	if err != nil {
		return nil, fmt.Errorf("cannot generate socket directory name: %w", err)
	}
	runDir := filepath.Join(baseDir, socketRunDirPrefix+suffix)
	if err := os.Mkdir(runDir, 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(runDir, socketPIDFile), []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600); err != nil {
		_ = os.RemoveAll(runDir)
		return nil, err
	}
	return &sshSocketTarget{
		SocketPath:   filepath.Join(runDir, "server.sock"),
		RunDir:       runDir,
		RemoveRunDir: true,
	}, nil
}

func cleanupSSHSocketTarget(socketPath, runDir string, removeRunDir bool) {
	if socketPath != "" {
		if err := os.Remove(socketPath); err != nil && !os.IsNotExist(err) {
			log.Printf("could not remove socket %q: %v", socketPath, err)
		}
	}
	if removeRunDir && runDir != "" {
		if err := os.RemoveAll(runDir); err != nil {
			log.Printf("could not remove socket run dir %q: %v", runDir, err)
		}
	}
}

func (t *sshSocketTarget) cleanup() {
	if t == nil {
		return
	}
	cleanupSSHSocketTarget(t.SocketPath, t.RunDir, t.RemoveRunDir)
}

func startCleanupWatcher(target *sshSocketTarget) (*exec.Cmd, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(
		exe,
		cleanupWatcherCommand,
		strconv.Itoa(os.Getpid()),
		target.SocketPath,
		target.RunDir,
		strconv.FormatBool(target.RemoveRunDir),
	)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return cmd, nil
}

func runCleanupWatcher(args []string) int {
	if len(args) != 4 {
		return 2
	}
	pid, err := strconv.Atoi(args[0])
	if err != nil {
		return 2
	}
	removeRunDir, err := strconv.ParseBool(args[3])
	if err != nil {
		return 2
	}
	for processAlive(pid) {
		time.Sleep(time.Second)
	}
	cleanupSSHSocketTarget(args[1], args[2], removeRunDir)
	return 0
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	for _, r := range s {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') ||
			(r >= '0' && r <= '9') || strings.ContainsRune("@%_+=:,./-", r) {
			continue
		}
		return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
	}
	return s
}

func sshTunnelCommand(sshHost string, localPort int, socketPath string) string {
	forward := fmt.Sprintf("127.0.0.1:%d:%s", localPort, socketPath)
	return fmt.Sprintf(
		"ssh -N -o ExitOnForwardFailure=yes -L %s %s",
		shellQuote(forward),
		shellQuote(sshHost),
	)
}
