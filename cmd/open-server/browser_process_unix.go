//go:build !windows

package main

import (
	"os/exec"
	"syscall"
)

func configureBrowserCommand(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}
