//go:build !windows

package tensorboard

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

func TestRemoteTensorBoardScriptStopsOnStdinEOF(t *testing.T) {
	command := exec.Command("sh", "-c", remoteTensorBoardScript("sleep", []string{"30"}))
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	command.Stdin = reader
	if err := command.Start(); err != nil {
		_ = reader.Close()
		_ = writer.Close()
		t.Fatal(err)
	}
	_ = reader.Close()
	process := newRunningProcess(command, writer)
	assertProcessRunning(t, process)

	started := time.Now()
	process.stop()
	if elapsed := time.Since(started); elapsed >= 2*time.Second {
		t.Fatalf("control-pipe shutdown took %s; remote wrapper did not exit promptly", elapsed)
	}
}

func TestLocalTensorBoardStopTerminatesProcessGroup(t *testing.T) {
	command := exec.Command("sh", "-c", "sleep 30 & wait")
	configureCommand(command)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	processGroup := command.Process.Pid
	process := newRunningProcess(command, nil)
	assertProcessRunning(t, process)
	process.stop()

	if err := syscall.Kill(-processGroup, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("TensorBoard process group %d survived shutdown: %v", processGroup, err)
	}
}

func assertProcessRunning(t *testing.T, process *runningProcess) {
	t.Helper()
	select {
	case <-process.done:
		t.Fatal("test process exited before shutdown")
	case <-time.After(100 * time.Millisecond):
	}
}
