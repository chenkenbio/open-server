package main

import (
	"context"
	"fmt"
	"io"
	"time"

	"open-server/internal/filesystem"
	"open-server/internal/sshsession"
)

// Reconnect policy defaults. A dropped SSH session is retried up to
// reconnectMaxAttempts times with exponential backoff from reconnectBaseDelay,
// capped at reconnectMaxDelay. defaultReconnectGrace bounds how long a session
// may stay disconnected before giving up; it is consumed by a later step.
const (
	reconnectMaxAttempts  = 10
	reconnectBaseDelay    = time.Second
	reconnectMaxDelay     = 5 * time.Minute
	defaultReconnectGrace = 30 * time.Minute
)

// reconnectPolicy captures the tunable backoff schedule so tests can drive the
// loop with tiny delays instead of the production defaults.
type reconnectPolicy struct {
	maxAttempts int
	baseDelay   time.Duration
	maxDelay    time.Duration
}

func defaultReconnectPolicy() reconnectPolicy {
	return reconnectPolicy{maxAttempts: reconnectMaxAttempts, baseDelay: reconnectBaseDelay, maxDelay: reconnectMaxDelay}
}

// nextBackoff returns the delay to wait before the next attempt. The first call
// passes a zero previous delay and receives the base delay; each later call
// doubles the previous delay, clamped to the policy maximum.
func nextBackoff(previous time.Duration, policy reconnectPolicy) time.Duration {
	next := policy.baseDelay
	if previous > 0 {
		next = previous * 2
	}
	if next > policy.maxDelay {
		next = policy.maxDelay
	}
	return next
}

// reconnectOutcome records why reconnectSSH stopped looping.
type reconnectOutcome int

const (
	reconnectConnected reconnectOutcome = iota
	reconnectExhausted
	reconnectCtxDone
	reconnectUserClose
)

// reconnectResult carries a reconnected session (only when connected) plus, when
// the loop ended because the user asked to exit, the time of that request.
type reconnectResult struct {
	session     *sshsession.Session
	outcome     reconnectOutcome
	requestedAt time.Time
}

// reconnectSSH repeatedly tries to re-establish the SSH/SFTP session behind a
// disconnected backend. Each attempt announces the upcoming wait and its
// exponential backoff first, then waits, interrupted by either ctx cancellation
// or a user close request on closeRequested. A freshly started session is
// accepted only after root is confirmed to be an existing directory; otherwise
// the session is closed and the attempt counts as a failure.
func reconnectSSH(ctx context.Context, options sshsession.Options, policy reconnectPolicy, root string, closeRequested <-chan time.Time, label string, output io.Writer) reconnectResult {
	var delay time.Duration
	for attempt := 1; attempt <= policy.maxAttempts; attempt++ {
		delay = nextBackoff(delay, policy)
		fmt.Fprintf(output, "Reconnecting to [%s] in %s (attempt %d/%d)…\n", label, delay, attempt, policy.maxAttempts)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return reconnectResult{outcome: reconnectCtxDone}
		case requestedAt := <-closeRequested:
			timer.Stop()
			return reconnectResult{outcome: reconnectUserClose, requestedAt: requestedAt}
		case <-timer.C:
		}
		session, err := sshsession.Start(ctx, options)
		if err != nil {
			if ctx.Err() != nil {
				return reconnectResult{outcome: reconnectCtxDone}
			}
			continue
		}
		backend := filesystem.SFTP{Client: session.Client}
		info, err := backend.Stat(ctx, root)
		if err != nil || !info.IsDir() {
			_ = session.Close()
			if ctx.Err() != nil {
				return reconnectResult{outcome: reconnectCtxDone}
			}
			continue
		}
		return reconnectResult{session: session, outcome: reconnectConnected}
	}
	return reconnectResult{outcome: reconnectExhausted}
}
