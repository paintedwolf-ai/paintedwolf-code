package observations

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	wire "github.com/lycaon/lycaon/pkg/api"
	"strings"
	"time"
)

var errProgressStoreRequired = errors.New("progress transcript store not configured")

// ActiveRunID returns the leaf workflow run for a session.
type ActiveRunID func(ctx context.Context, sessionID string) string

func activeRunIDFromWorkflow(runs runstate.RunsRepository) ActiveRunID {
	return func(ctx context.Context, sessionID string) string {
		if runs == nil {
			return ""
		}
		run, err := runs.ActiveBySession(ctx, sessionID)
		if err != nil || run == nil {
			return ""
		}
		return strings.TrimSpace(run.ID)
	}
}

// appendProgressTranscript stamps the active run and persists. written is false when there is no run.
func appendProgressTranscript(
	ctx context.Context,
	store *store.SQL,
	eventPub *events.Publisher,
	activeRun ActiveRunID,
	progressStore progress.RunScopedStore,
	sessionID string,
	msg wire.Message,
) (written bool, err error) {
	if store == nil || activeRun == nil {
		return false, nil
	}
	runID := strings.TrimSpace(activeRun(ctx, sessionID))
	if runID == "" {
		return false, nil
	}
	msg.WorkflowRunID = runID
	if msg.ID == "" {
		msg.ID = uuid.NewString()
	}
	if msg.CreatedAt.IsZero() {
		msg.CreatedAt = time.Now().UTC()
	}
	// Unbound docs bind on first emit; run changes reset on phase enter.
	if progressStore != nil && progressStore.BoundRunID(sessionID) == "" {
		progressStore.BindRun(sessionID, runID)
	}
	if err := appendAndPublishMessage(ctx, store, eventPub, sessionID, msg); err != nil {
		return false, err
	}
	return true, nil
}
