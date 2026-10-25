package db

import "context"

// A full audit snapshot and one bounded maintenance pass share this turn.
// Foreground reads, writes and checkpoints never acquire it. Audit retries
// release the turn before backoff, allowing waiting maintenance to reclaim space.
func (a *auditControl) backgroundTurn(ctx context.Context) (func(), error) {
	a.mu.Lock()
	if a.background == nil {
		a.background = make(chan struct{}, 1)
	}
	gate := a.background
	a.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case gate <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-gate
			return nil, err
		}
		return func() { <-gate }, nil
	}
}

func maintenanceTurn(ctx context.Context, database Handle) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if store, ok := database.(*Store); ok {
		a := &store.audit
		a.mu.Lock()
		a.maintenanceWaiting++
		if a.cancel != nil && a.frameLimit == 0 {
			a.cancel(errAuditDeferred)
		}
		a.mu.Unlock()
		release, err := a.backgroundTurn(ctx)
		a.mu.Lock()
		a.maintenanceWaiting--
		a.mu.Unlock()
		return release, err
	}
	return func() {}, nil
}

// Shrinking free space releases the audit even when no new WAL frames appear.
// Sampling cannot promise a hard physical cap between observations.
func (a *auditControl) resourceLimit(limit uint64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cancel == nil {
		return
	}
	a.frameLimit = min(a.frameLimit, limit)
	if limit == 0 && a.maintenanceWaiting > 0 {
		a.cancel(errAuditDeferred)
	}
}
