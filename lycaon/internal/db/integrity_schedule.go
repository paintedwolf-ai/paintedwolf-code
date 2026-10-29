package db

import (
	"context"
	"errors"
	"sync"
	"time"
)

var errAuditDeferred = errors.New("integrity audit yielded to checkpoint pressure")

// auditControl connects the existing checkpointer to the current audit read.
// It never interrupts application reads or writes.
type auditControl struct {
	run                sync.Mutex
	mu                 sync.Mutex
	cancel             context.CancelCauseFunc
	frameLimit         uint64
	background         chan struct{}
	completed          chan struct{}
	maintenanceWaiting int
}

func (a *auditControl) begin(parent context.Context, frameLimit uint64) (context.Context, func()) {
	ctx, cancel := context.WithCancelCause(parent)
	a.mu.Lock()
	a.cancel = cancel
	a.frameLimit = frameLimit
	if frameLimit == 0 && a.maintenanceWaiting > 0 {
		cancel(errAuditDeferred)
	}
	a.mu.Unlock()
	return ctx, func() {
		a.mu.Lock()
		a.cancel = nil
		a.mu.Unlock()
		cancel(nil)
	}
}

func (a *auditControl) active() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cancel != nil
}

// SQLite's checkpoint result counts reused frames as well as file growth.
// This is sampled pressure, not an absolute bound between checkpoints, and
// other readers can retain these frames after the audit releases its snapshot.
func (a *auditControl) checkpoint(result WALCheckpoint) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cancel != nil && result.PinnedFrames() > 0 && uint64(result.PinnedFrames()) > a.frameLimit { // #nosec G115
		a.cancel(errAuditDeferred)
	}
}

func (a *auditControl) interrupt(cause error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cancel != nil {
		a.cancel(cause)
	}
}

// Only a complete whole-file snapshot discharges the audit obligation.
func auditAttempts(ctx context.Context, check func(context.Context) error, wait func(context.Context) error) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := check(ctx)
		if !errors.Is(err, errAuditDeferred) {
			return err
		}
		if err := wait(ctx); err != nil {
			return err
		}
	}
}

// Backoff spaces attempts without requiring writes to stop. Every wake gets
// another chance even if unrelated readers prevent a complete checkpoint.
func waitForAuditCapacity(ctx context.Context, store *Store, delay time.Duration) error {
	return waitForAuditRetry(ctx, time.After(delay), func() error {
		_, err := CheckpointWAL(ctx, store)
		return err
	})
}

func waitForAuditRetry(ctx context.Context, wake <-chan time.Time, checkpoint func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-wake:
		return checkpoint()
	}
}

func auditRetryDelay(attempt int) time.Duration {
	if attempt > 5 {
		return 30 * time.Second
	}
	if attempt < 1 {
		attempt = 1
	}
	return time.Duration(1<<(attempt-1)) * time.Second
}
