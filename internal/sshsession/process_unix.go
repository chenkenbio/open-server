//go:build !windows

package sshsession

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

func configureProcess(_ *exec.Cmd) {}

func signalProcess(cmd *exec.Cmd, signal os.Signal) error {
	if cmd.Process == nil {
		return os.ErrProcessDone
	}
	descendants := descendantPIDs(cmd.Process.Pid)
	err := cmd.Process.Signal(signal)
	if sig, ok := signal.(syscall.Signal); ok {
		for _, pid := range descendants {
			_ = syscall.Kill(pid, sig)
		}
	}
	return err
}

func killProcess(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return os.ErrProcessDone
	}
	descendants := descendantPIDs(cmd.Process.Pid)
	err := cmd.Process.Kill()
	for _, pid := range descendants {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
	return err
}

func descendantPIDs(root int) []int {
	output, err := exec.Command("/bin/ps", "-axo", "pid=,ppid=").Output()
	if err != nil {
		return nil
	}
	children := make(map[int][]int)
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		pid, pidErr := strconv.Atoi(fields[0])
		parent, parentErr := strconv.Atoi(fields[1])
		if pidErr == nil && parentErr == nil {
			children[parent] = append(children[parent], pid)
		}
	}
	queue := append([]int(nil), children[root]...)
	descendants := make([]int, 0, len(queue))
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		descendants = append(descendants, pid)
		queue = append(queue, children[pid]...)
	}
	return descendants
}
