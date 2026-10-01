package execution

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	scanbase "github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/pkg/api"
)

func (r *Runner) publishWarming(ctx context.Context) {
	if r == nil || r.Store == nil || r.Coordinator == nil {
		return
	}
	ids, err := r.Store.WarmingObligationIDs(ctx, 8)
	if err != nil {
		slog.WarnContext(ctx, "list warming scan obligations", "error", err)
		return
	}
	for _, id := range ids {
		if !r.publicationDue(id) {
			continue
		}
		err := r.Coordinator.PublishPending(ctx, id)
		if err == nil {
			r.clearPublishAttempts(id)
			continue
		}
		if ctx.Err() != nil {
			return
		}
		attempts := r.notePublishFailure(id)
		slog.WarnContext(ctx, "publish warming scan obligation",
			"scan_id", id, "attempt", attempts, "max_attempts", maxPublishAttempts, "error", err)
		if attempts < maxPublishAttempts {
			// Retry at the publication deadline even if no new work arrives.
			continue
		}
		r.clearPublishAttempts(id)
		if rec, getErr := r.Store.Get(ctx, id); getErr == nil && rec != nil {
			message := fmt.Sprintf("publish source snapshot: %v", err)
			won, markErr := r.Store.FinalizePendingFailure(ctx, id, scanbase.FailureSourceUnavailable, message)
			if markErr != nil || !won {
				continue
			}
			rec.Status = api.CodeScanStatusFailed
			rec.Error = message
			now := time.Now().UTC()
			rec.CompletedAt = &now
			r.notifyTerminal(ctx, rec)
		}
	}
}

func (r *Runner) notePublishFailure(id string) int {
	r.publishMu.Lock()
	defer r.publishMu.Unlock()
	if r.publishAttempts == nil {
		r.publishAttempts = map[string]int{}
		r.publishAfter = map[string]time.Time{}
	}
	r.publishAttempts[id]++
	r.publishAfter[id] = time.Now().Add(r.retryDelay())
	return r.publishAttempts[id]
}

func (r *Runner) clearPublishAttempts(id string) {
	r.publishMu.Lock()
	defer r.publishMu.Unlock()
	delete(r.publishAttempts, id)
	delete(r.publishAfter, id)
}
