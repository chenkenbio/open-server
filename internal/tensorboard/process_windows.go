//go:build windows

package tensorboard

import (
	"os"
	"os/exec"
)

func configureCommand(_ *exec.Cmd) {}

func terminateCommand(command *exec.Cmd) error {
	if command.Process == nil {
		return os.ErrProcessDone
	}
	return command.Process.Kill()
}

func killCommand(command *exec.Cmd) error {
	if command.Process == nil {
		return os.ErrProcessDone
	}
	return command.Process.Kill()
}
