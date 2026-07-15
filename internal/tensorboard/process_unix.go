//go:build !windows

package tensorboard

import (
	"os"
	"os/exec"
	"syscall"
)

func configureCommand(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func terminateCommand(command *exec.Cmd) error {
	if command.Process == nil {
		return os.ErrProcessDone
	}
	return syscall.Kill(-command.Process.Pid, syscall.SIGTERM)
}

func killCommand(command *exec.Cmd) error {
	if command.Process == nil {
		return os.ErrProcessDone
	}
	return syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
}
