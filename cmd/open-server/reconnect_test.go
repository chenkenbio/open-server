package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"open-server/internal/sshsession"
)

func TestNextBackoff(t *testing.T) {
	t.Parallel()
	policy := reconnectPolicy{maxAttempts: 10, baseDelay: time.Second, maxDelay: 5 * time.Minute}
	tests := []struct {
		previous time.Duration
		want     time.Duration
	}{
		{0, time.Second},
		{time.Second, 2 * time.Second},
		{2 * time.Second, 4 * time.Second},
		{4 * time.Second, 8 * time.Second},
		{16 * time.Second, 32 * time.Second},
		{2 * time.Minute, 4 * time.Minute},
		{4 * time.Minute, 5 * time.Minute},
		{5 * time.Minute, 5 * time.Minute},
	}
	for _, test := range tests {
		if got := nextBackoff(test.previous, policy); got != test.want {
			t.Errorf("nextBackoff(%s) = %s, want %s", test.previous, got, test.want)
		}
	}
}

func TestReconnectSSHExhausted(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	policy := reconnectPolicy{maxAttempts: 3, baseDelay: time.Millisecond, maxDelay: 5 * time.Millisecond}
	options := sshsession.Options{Executable: failingWrapper(t), Host: "test-host", Stderr: io.Discard}
	result := reconnectSSH(context.Background(), options, policy, "/", make(chan time.Time), "test-host:.", &output)
	if result.outcome != reconnectExhausted {
		t.Fatalf("outcome = %d, want exhausted", result.outcome)
	}
	if !strings.Contains(output.String(), "Reconnecting to [test-host:.] in 4ms (attempt 3/3)") {
		t.Fatalf("output did not report every attempt: %q", output.String())
	}
}

func TestReconnectSSHContextDone(t *testing.T) {
	t.Parallel()
	policy := reconnectPolicy{maxAttempts: 5, baseDelay: 500 * time.Millisecond, maxDelay: time.Second}
	options := sshsession.Options{Executable: failingWrapper(t), Host: "test-host", Stderr: io.Discard}
	ctx, cancel := context.WithCancel(context.Background())
	outcomes := make(chan reconnectOutcome, 1)
	go func() {
		outcomes <- reconnectSSH(ctx, options, policy, "/", make(chan time.Time), "test-host:.", io.Discard).outcome
	}()
	time.AfterFunc(20*time.Millisecond, cancel)
	select {
	case outcome := <-outcomes:
		if outcome != reconnectCtxDone {
			t.Fatalf("outcome = %d, want ctxDone", outcome)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reconnect did not return after context cancellation")
	}
}

func TestReconnectSSHUserClose(t *testing.T) {
	t.Parallel()
	policy := reconnectPolicy{maxAttempts: 5, baseDelay: 500 * time.Millisecond, maxDelay: time.Second}
	options := sshsession.Options{Executable: failingWrapper(t), Host: "test-host", Stderr: io.Discard}
	closeRequested := make(chan time.Time, 1)
	requestedAt := time.Now()
	closeRequested <- requestedAt
	result := reconnectSSH(context.Background(), options, policy, "/", closeRequested, "test-host:.", io.Discard)
	if result.outcome != reconnectUserClose {
		t.Fatalf("outcome = %d, want userClose", result.outcome)
	}
	if !result.requestedAt.Equal(requestedAt) {
		t.Fatalf("requestedAt = %s, want %s", result.requestedAt, requestedAt)
	}
}

func TestReconnectSSHReconnects(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	var output bytes.Buffer
	policy := reconnectPolicy{maxAttempts: 3, baseDelay: time.Millisecond, maxDelay: 5 * time.Millisecond}
	options := sshsession.Options{Executable: cliWrapper(t, false), Host: "test-host", Stderr: io.Discard}
	result := reconnectSSH(context.Background(), options, policy, root, make(chan time.Time), "test-host:.", &output)
	if result.outcome != reconnectConnected {
		t.Fatalf("outcome = %d, want connected; output = %q", result.outcome, output.String())
	}
	if result.session == nil {
		t.Fatal("connected outcome returned a nil session")
	}
	_ = result.session.Close()
	if !strings.Contains(output.String(), "Reconnecting to [test-host:.] in 1ms (attempt 1/3)") {
		t.Fatalf("output missing attempt notice: %q", output.String())
	}
}

func failingWrapper(t *testing.T) string {
	t.Helper()
	wrapper := filepath.Join(t.TempDir(), "ssh-wrapper")
	if err := os.WriteFile(wrapper, []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return wrapper
}
