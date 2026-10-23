package db

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"time"
)

const auditPinnedFrameLimit = 1024

var errAuditDeferred = errors.New("integrity audit yielded to checkpoint pressure")

// auditControl connects the existing checkpointer to the current audit read.
// It never interrupts application reads or writes.
type auditControl struct {
	run    sync.Mutex
	mu     sync.Mutex
	cancel context.CancelCauseFunc
}

func (a *auditControl) begin(parent context.Context) (context.Context, func()) {
	ctx, cancel := context.WithCancelCause(parent)
	a.mu.Lock()
	a.cancel = cancel
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

func (a *auditControl) checkpoint(result WALCheckpoint) {
	if result.PinnedFrames() < auditPinnedFrameLimit {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cancel != nil {
		a.cancel(errAuditDeferred)
	}
}

// Completed tables leave the queue. Deferred tables retry in this boot once
// writes quiet down and their released snapshots can be checkpointed.
func auditTables(ctx context.Context, database *sql.DB, audited func(string), check func(context.Context, string) error, wait func(context.Context) error) error {
	remaining, err := auditableTables(ctx, database)
	if err != nil {
		return integrityCheckError(err, "list tables")
	}
	for len(remaining) > 0 {
		var deferred []string
		for _, table := range remaining {
			if err := ctx.Err(); err != nil {
				return err
			}
			err := check(ctx, table)
			if errors.Is(err, errAuditDeferred) {
				deferred = append(deferred, table)
				continue
			}
			if err != nil {
				return err
			}
			if audited != nil {
				audited(table)
			}
		}
		remaining = deferred
		if len(remaining) > 0 {
			if err := wait(ctx); err != nil {
				return err
			}
		}
	}
	return nil
}

func waitForAuditCapacity(ctx context.Context, store *Store) error {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	writes := store.writeCount.Load()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			current := store.writeCount.Load()
			quiet := current == writes
			writes = current
			if !quiet {
				continue
			}
			result, err := CheckpointWAL(ctx, store)
			if err != nil {
				return err
			}
			if result.Complete() {
				return nil
			}
		}
	}
}
