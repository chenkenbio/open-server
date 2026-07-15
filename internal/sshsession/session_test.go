package sshsession

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pkg/sftp"
)

func TestCommandDisablesUnrelatedSSHChannels(t *testing.T) {
	t.Parallel()
	executable, args, err := Command("/custom/ssh-wrapper", "lab-alias")
	if err != nil {
		t.Fatal(err)
	}
	if executable != "/custom/ssh-wrapper" {
		t.Fatalf("executable = %q", executable)
	}
	want := []string{
		"-T", "-x", "-a",
		"-o", "ClearAllForwardings=yes",
		"-o", "ForkAfterAuthentication=no",
		"-o", "ControlMaster=no",
		"-o", "ControlPersist=no",
		"-o", "PermitLocalCommand=no",
		"-o", "RemoteCommand=none",
		"-o", "StdinNull=no",
		"-o", "SessionType=subsystem",
		"lab-alias", "sftp",
	}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %#v, want %#v", args, want)
	}
}

func TestSessionStopsWithContextAndExplicitClose(t *testing.T) {
	wrapper := helperWrapper(t, false)

	t.Run("context deadline", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		session, err := Start(ctx, Options{Executable: wrapper, Host: "test-host", Stderr: io.Discard})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := session.Client.Getwd(); err != nil {
			t.Fatalf("SFTP operation failed: %v", err)
		}
		select {
		case <-session.Done():
		case <-time.After(3 * time.Second):
			t.Fatal("SSH-compatible wrapper survived context deadline")
		}
		_ = session.Close()
	})

	t.Run("explicit close", func(t *testing.T) {
		session, err := Start(context.Background(), Options{Executable: wrapper, Host: "test-host", Stderr: io.Discard})
		if err != nil {
			t.Fatal(err)
		}
		started := time.Now()
		_ = session.Close()
		if time.Since(started) > 3*time.Second {
			t.Fatal("explicit session close took too long")
		}
		select {
		case <-session.Done():
		default:
			t.Fatal("process was still running after Close")
		}
	})
}

func TestSessionDetectsSFTPTransportLoss(t *testing.T) {
	session, err := Start(context.Background(), Options{Executable: helperWrapper(t, true), Host: "test-host", Stderr: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-session.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("session did not detect SFTP transport loss")
	}
	select {
	case <-session.processDone:
		t.Fatal("helper process exited; test did not isolate transport monitoring")
	default:
	}
	_ = session.Close()
}

func TestSFTPHelperProcess(t *testing.T) {
	if os.Getenv("OPEN_SERVER_SFTP_HELPER") != "1" {
		return
	}
	server, err := sftp.NewServer(struct {
		io.Reader
		io.WriteCloser
	}{os.Stdin, os.Stdout})
	if err != nil {
		os.Exit(2)
	}
	if os.Getenv("OPEN_SERVER_SFTP_DROP") == "1" {
		go func() { _ = server.Serve() }()
		time.Sleep(300 * time.Millisecond)
		_ = os.Stdout.Close()
		time.Sleep(10 * time.Second)
		os.Exit(0)
	}
	_ = server.Serve()
	_ = server.Close()
	os.Exit(0)
}

func helperWrapper(t *testing.T, dropTransport bool) string {
	t.Helper()
	name := filepath.Join(t.TempDir(), "ssh-wrapper")
	drop := ""
	if dropTransport {
		drop = " OPEN_SERVER_SFTP_DROP=1"
	}
	contents := "#!/bin/sh\nOPEN_SERVER_SFTP_HELPER=1" + drop + " " + shellQuote(os.Args[0]) + " -test.run='^TestSFTPHelperProcess$' <&0 >&1 2>&2 &\nchild=$!\nexec >/dev/null 2>/dev/null\nwait \"$child\"\n"
	if err := os.WriteFile(name, []byte(contents), 0o700); err != nil {
		t.Fatal(err)
	}
	return name
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func TestClassifyStartError(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"Permission denied (publickey).":                          "authentication",
		"Host key verification failed.":                           "host-key",
		"subsystem request failed on channel 0":                   "SFTP subsystem",
		"ssh: Could not resolve hostname nowhere: nodename":       "initialize",
		"WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED!":        "host-key",
		"The requested subsystem was not found on the SSH server": "subsystem",
	}
	for diagnostic, want := range tests {
		if got := classifyStartError(diagnostic).Error(); !containsFold(got, want) {
			t.Errorf("classifyStartError(%q) = %q, want to contain %q", diagnostic, got, want)
		}
	}
}

func containsFold(value, part string) bool {
	if len(part) > len(value) {
		return false
	}
	for i := 0; i+len(part) <= len(value); i++ {
		match := true
		for j := range part {
			a, b := value[i+j], part[j]
			if a >= 'A' && a <= 'Z' {
				a += 'a' - 'A'
			}
			if b >= 'A' && b <= 'Z' {
				b += 'a' - 'A'
			}
			if a != b {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
