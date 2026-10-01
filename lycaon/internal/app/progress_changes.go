package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/session/store"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// newProgressChangeEmitter persists coalesced nonterminal changes.
// ChangedAt keeps transcript order tied to the write.
func newProgressChangeEmitter(
	store *store.SQL,
	eventPub *events.Publisher,
	activeRun ActiveRunID,
	progressStore progress.RunScopedStore,
) progress.FlushFunc {
	if store == nil || eventPub == nil || activeRun == nil {
		return func(progress.FlushPayload) {}
	}
	return func(p progress.FlushPayload) {
		meta, ok := buildProgressUpdate(p.Baseline, p.Latest, p.Seq)
		if !ok {
			return
		}
		changedAt := p.ChangedAt
		if changedAt.IsZero() {
			changedAt = time.Now().UTC()
		}
		msg := wire.Message{
			ID:             uuid.NewString(),
			Role:           wire.MessageRoleSystem,
			Kind:           wire.MessageKindProgressUpdate,
			Visibility:     wire.MessageVisibilityTranscript,
			ProgressUpdate: meta,
			CreatedAt:      changedAt.UTC(),
		}
		if _, err := appendProgressTranscript(context.Background(), store, eventPub, activeRun, progressStore, p.SessionID, msg); err != nil {
			slog.Error("progress_update transcript append failed",
				"session_id", p.SessionID, "error", err)
		}
	}
}

// buildProgressUpdate omits terminal windows and summarizes large diffs.
func buildProgressUpdate(baseline, latest string, seq int) (*wire.ProgressUpdateMeta, bool) {
	baselineSteps := progress.DeriveProgress(baseline, progress.DefaultProgressCap).Items
	latestSteps := progress.DeriveProgress(latest, progress.DefaultProgressCap).Items

	if progress.AllTerminal(latest) && !progress.AllTerminal(baseline) {
		return nil, false
	}
	changes := progress.DiffSteps(baselineSteps, latestSteps)
	if len(changes) == 0 {
		return nil, false
	}

	meta := &wire.ProgressUpdateMeta{Seq: seq}
	if len(baselineSteps) == 0 {
		meta.Initial = true
		if len(latestSteps) > progress.MaxDetailedProgressUpdateRows {
			summary := progress.SummarizeUpdate(latestSteps, len(changes))
			meta.Summary = &summary
		} else {
			meta.Steps = latestSteps
		}
	} else if len(changes) > progress.MaxDetailedProgressUpdateRows {
		summary := progress.SummarizeUpdate(latestSteps, len(changes))
		meta.Summary = &summary
	} else {
		meta.Changes = changes
	}
	return meta, true
}
