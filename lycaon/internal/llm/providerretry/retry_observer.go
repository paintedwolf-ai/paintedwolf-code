package providerretry

import (
	"context"
	"log/slog"
	"time"
)

// RetryAttempt describes an upcoming provider retry.
type RetryAttempt struct {
	// Attempt is the 1-based index of the attempt about to run: the first retry
	// is attempt 2.
	Attempt int
	// MaxAttempts is zero while waiting without an attempt limit.
	MaxAttempts int
	// Wait is the backoff actually slept before this attempt.
	Wait time.Duration
	// Status is the HTTP status that triggered the retry, or 0 for a fault
	// that never carried one.
	Status int
	// Reason identifies the retry trigger.
	Reason RetryReason
	// Silence is how long the provider held the previous request without
	// answering. Zero for every reason other than a silent provider.
	Silence time.Duration
}

// RetryReason names why a request is being reissued.
type RetryReason string

const (
	RetryReasonStatus   RetryReason = "status"
	RetryReasonCapacity RetryReason = "capacity"
	RetryReasonHold     RetryReason = "hold"
	RetryReasonCooldown RetryReason = "cooldown"
	// RetryReasonUnreachable is a request that never reached the provider.
	RetryReasonUnreachable RetryReason = "unreachable"
	// RetryReasonSilent is a delivered request the provider never answered.
	RetryReasonSilent RetryReason = "silent"
	// RetryReasonEmptyCompletion is a terminal response without usable output.
	RetryReasonEmptyCompletion RetryReason = "empty_completion"
)

type retryObserverKey struct{}

// RetryObserver receives each retry as it is scheduled.
type RetryObserver func(RetryAttempt)

// WithRetryObserver installs a retry observer on ctx.
func WithRetryObserver(ctx context.Context, fn RetryObserver) context.Context {
	if fn == nil {
		return ctx
	}
	return context.WithValue(ctx, retryObserverKey{}, fn)
}

// ObserveRetry logs and reports a retry.
func ObserveRetry(ctx context.Context, a RetryAttempt) {
	slog.WarnContext(ctx, "llm provider retry",
		"attempt", a.Attempt,
		"max_attempts", a.MaxAttempts,
		"wait_ms", a.Wait.Milliseconds(),
		"status", a.Status,
		"reason", string(a.Reason),
		"silence_ms", a.Silence.Milliseconds(),
	)
	fn, _ := ctx.Value(retryObserverKey{}).(RetryObserver)
	if fn == nil {
		return
	}
	fn(a)
}
