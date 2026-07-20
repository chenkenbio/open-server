//go:build windows

package main

import (
	"os/exec"
	"syscall"
)

func configureBrowserCommand(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP,
	}
}
