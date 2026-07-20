//go:build !windows

package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestConfigureBrowserCommandStartsNewProcessGroup(t *testing.T) {
	command := exec.Command(os.Args[0])
	configureBrowserCommand(command)
	if command.SysProcAttr == nil || !command.SysProcAttr.Setpgid {
		t.Fatalf("browser process attributes = %#v, want Setpgid", command.SysProcAttr)
	}
}

func TestRunSignalLeavesBrowserRunning(t *testing.T) {
	stateDirectory := t.TempDir()
	pidPath := filepath.Join(stateDirectory, "browser.pid")
	probePath := filepath.Join(stateDirectory, "browser.probe")
	ackPath := filepath.Join(stateDirectory, "browser.ack")
	openerDirectory := t.TempDir()
	openerName := "xdg-open"
	if runtime.GOOS == "darwin" {
		openerName = "open"
	}
	openerPath := filepath.Join(openerDirectory, openerName)
	opener := "#!/bin/sh\nexec " + quoteShell(os.Args[0]) + " -test.run='^TestBrowserHelperProcess$'\n"
	if err := os.WriteFile(openerPath, []byte(opener), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", openerDirectory+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("OPEN_SERVER_BROWSER_HELPER", "1")
	t.Setenv("OPEN_SERVER_BROWSER_PID", pidPath)
	t.Setenv("OPEN_SERVER_BROWSER_PROBE", probePath)
	t.Setenv("OPEN_SERVER_BROWSER_ACK", ackPath)

	output := newNotifyWriter("Open this local URL")
	result := make(chan error, 1)
	root := t.TempDir()
	go func() {
		result <- run([]string{"--duration", "15s", root}, output)
	}()
	select {
	case <-output.ready:
	case <-time.After(5 * time.Second):
		t.Fatalf("session did not start: %s", output.String())
	}

	var browserPID int
	deadline := time.Now().Add(5 * time.Second)
	for browserPID == 0 {
		contents, err := os.ReadFile(pidPath)
		if err == nil {
			browserPID, err = strconv.Atoi(strings.TrimSpace(string(contents)))
			if err != nil {
				t.Fatalf("parse browser PID %q: %v", contents, err)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("browser helper did not start: %s", output.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	defer func() { _ = syscall.Kill(browserPID, syscall.SIGKILL) }()

	browserGroup, err := syscall.Getpgid(browserPID)
	if err != nil {
		t.Fatal(err)
	}
	if browserGroup == syscall.Getpgrp() {
		t.Fatalf("browser process group = %d, want a group separate from open-server", browserGroup)
	}

	process, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if err := process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("signal did not shut down the session")
	}

	time.Sleep(100 * time.Millisecond)
	if err := os.WriteFile(probePath, []byte("after shutdown"), 0o600); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(ackPath); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("browser process %d did not respond after server shutdown", browserPID)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestBrowserHelperProcess(t *testing.T) {
	if os.Getenv("OPEN_SERVER_BROWSER_HELPER") != "1" {
		return
	}
	pidPath := os.Getenv("OPEN_SERVER_BROWSER_PID")
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}
	probePath := os.Getenv("OPEN_SERVER_BROWSER_PROBE")
	ackPath := os.Getenv("OPEN_SERVER_BROWSER_ACK")
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(probePath); err == nil {
			if err := os.WriteFile(ackPath, []byte("alive"), 0o600); err != nil {
				t.Fatal(err)
			}
			return
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("browser helper timed out waiting for a probe")
}
