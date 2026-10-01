package execution

import (
	"context"
	"log/slog"
	"time"

	scancfg "github.com/lycaon/lycaon/internal/scan/configuration"
)

func (r *Runner) retryDelay() time.Duration {
	if r.RetryDelay > 0 {
		return r.RetryDelay
	}
	return time.Duration(scancfg.DefaultRunnerConfig().Runner.RetryDelayMs) * time.Millisecond
}

func (r *Runner) publicationDue(id string) bool {
	r.publishMu.Lock()
	defer r.publishMu.Unlock()
	return !r.publishAfter[id].After(time.Now())
}

func (r *Runner) nextWake(ctx context.Context) time.Duration {
	delay := r.ReconcileInterval
	if delay <= 0 {
		delay = scancfg.DefaultRunnerConfig().ReconcileInterval()
	}
	expiry, err := r.Store.NextLeaseExpiry(ctx)
	if err != nil {
		slog.WarnContext(ctx, "read scan lease deadline", "error", err)
		return min(delay, r.retryDelay())
	}
	delay = sooner(delay, expiry)
	if r.Settings != nil && !r.Settings.Effective().Enabled {
		return delay
	}
	if r.Coordinator == nil {
		return delay
	}
	ids, err := r.Store.WarmingObligationIDs(ctx, 8)
	if err != nil {
		slog.WarnContext(ctx, "read warming scan deadlines", "error", err)
		return min(delay, r.retryDelay())
	}
	r.publishMu.Lock()
	defer r.publishMu.Unlock()
	for _, id := range ids {
		at := r.publishAfter[id]
		if at.IsZero() {
			return 0
		}
		delay = sooner(delay, at)
	}
	return delay
}

func waitForWork(ctx context.Context, wake, settingsChanged <-chan struct{}, delay time.Duration) {
	timer := time.NewTimer(max(delay, 0))
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-wake:
	case <-settingsChanged:
	case <-timer.C:
	}
}

func sooner(delay time.Duration, deadline time.Time) time.Duration {
	if deadline.IsZero() {
		return delay
	}
	return min(delay, max(time.Until(deadline), 0))
}
