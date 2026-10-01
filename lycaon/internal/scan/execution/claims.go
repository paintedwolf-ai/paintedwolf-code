package execution

import (
	"context"
	"log/slog"
	"time"

	scanbase "github.com/lycaon/lycaon/internal/scan"
	scancatalog "github.com/lycaon/lycaon/internal/scan/catalog"
	"github.com/lycaon/lycaon/pkg/api"
)

func (r *Runner) registerExecution(parent context.Context, job *api.CodeScan) (context.Context, *scanExecution) {
	ctx, cancel := context.WithCancel(parent)
	execution := &scanExecution{claimToken: job.ClaimToken, cancel: cancel}
	r.claimMu.Lock()
	if previous := r.claims[job.ID]; previous != nil {
		previous.cancel()
	}
	r.claims[job.ID] = execution
	r.claimMu.Unlock()
	return ctx, execution
}

func (r *Runner) unregisterExecution(scanID string, execution *scanExecution) {
	r.claimMu.Lock()
	if r.claims[scanID] == execution {
		delete(r.claims, scanID)
	}
	r.claimMu.Unlock()
	execution.cancel()
}

func (r *Runner) renewActiveClaims(ctx context.Context) {
	type activeClaim struct {
		id        string
		token     string
		execution *scanExecution
	}
	r.claimMu.Lock()
	claims := make([]activeClaim, 0, len(r.claims))
	for id, execution := range r.claims {
		claims = append(claims, activeClaim{id: id, token: execution.claimToken, execution: execution})
	}
	r.claimMu.Unlock()
	for _, claim := range claims {
		renewed, err := r.Store.RenewClaim(ctx, claim.id, claim.token)
		if err != nil || !renewed {
			claim.execution.cancel()
		}
	}
}

func (r *Runner) reconcileSoftRuntime(ctx context.Context, job *api.CodeScan, policy scancatalog.RuntimePolicy, startedAt time.Time) {
	softLimit := time.Duration(policy.Normalized().SoftLimitSec) * time.Second
	if time.Since(startedAt) < softLimit {
		return
	}
	at := time.Now().UTC()
	if _, err := r.Store.MarkLongRunning(ctx, job, at); err != nil {
		slog.WarnContext(ctx, "reconcile scan long-running state", "scan_id", job.ID, "error", err)
	}
}

func (r *Runner) scanClaimContext(parent context.Context, job *api.CodeScan) (context.Context, func()) {
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(scanbase.ScanClaimLease / 3)
		defer ticker.Stop()
		for {
			changed := r.Settings.Changed()
			if r.Settings != nil && !r.Settings.Effective().Enabled {
				cancel()
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-changed:
			case <-ticker.C:
				renewed, err := r.Store.RenewClaim(ctx, job.ID, job.ClaimToken)
				if err != nil || !renewed {
					cancel()
					return
				}
			}
		}
	}()
	return ctx, func() {
		cancel()
		<-done
	}
}

func (r *Runner) scannerContext(parent context.Context, job *api.CodeScan, policy scancatalog.RuntimePolicy) (context.Context, func()) {
	policy = policy.Normalized()
	ctx, cancel := policy.Context(parent)
	done := make(chan struct{})
	go func() {
		soft := time.NewTimer(time.Duration(policy.SoftLimitSec) * time.Second)
		defer soft.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-done:
				return
			case <-soft.C:
				at := time.Now().UTC()
				won, err := r.Store.MarkLongRunning(ctx, job, at)
				if err != nil {
					slog.WarnContext(ctx, "mark scan long-running", "scan_id", job.ID, "error", err)
				} else if !won {
					slog.DebugContext(ctx, "scan long-running transition already settled", "scan_id", job.ID)
				}
				slog.WarnContext(ctx, "scan exceeded soft runtime budget",
					"scan_id", job.ID, "scanner_id", job.ScannerID,
					"soft_limit_sec", policy.SoftLimitSec, "hard_limit_sec", policy.HardLimitSec)
			}
		}
	}()
	return ctx, func() {
		close(done)
		cancel()
	}
}
