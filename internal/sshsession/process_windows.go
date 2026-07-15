//go:build windows

package sshsession

import (
	"os"
	"os/exec"
)

func configureProcess(_ *exec.Cmd) {}

func signalProcess(cmd *exec.Cmd, signal os.Signal) error {
	if cmd.Process == nil {
		return os.ErrProcessDone
	}
	return cmd.Process.Signal(signal)
}

func killProcess(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return os.ErrProcessDone
	}
	return cmd.Process.Kill()
}
