package observations

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

func emitProgressCompletion(
	ctx context.Context,
	store *store.SQL,
	eventPub *events.Publisher,
	progressStore progress.RunScopedStore,
	activeRun ActiveRunID,
	sessionID string,
) {
	if store == nil || eventPub == nil || progressStore == nil || activeRun == nil {
		return
	}
	content := progressStore.Get(ctx, sessionID)
	reservation, ok := progress.ReserveCompletion(sessionID, progress.AllTerminal(content))
	if !ok {
		return
	}
	steps := progress.DeriveProgress(content, progress.DefaultProgressCap).Items
	if len(steps) == 0 {
		reservation.Abort()
		return
	}
	msg := wire.Message{
		ID:               uuid.NewString(),
		Role:             wire.MessageRoleSystem,
		Kind:             wire.MessageKindProgressComplete,
		Visibility:       wire.MessageVisibilityTranscript,
		ProgressComplete: &wire.ProgressCompleteMeta{Steps: steps, Seq: reservation.Seq},
		CreatedAt:        time.Now().UTC(),
	}
	written, err := appendProgressTranscript(ctx, store, eventPub, activeRun, progressStore, sessionID, msg)
	if err != nil {
		reservation.Abort()
		slog.ErrorContext(ctx, "progress_complete transcript append failed",
			"session_id", sessionID, "error", err)
		return
	}
	if !written {
		reservation.Abort()
		return
	}
	reservation.Commit()
}

// appendAndPublishMessage publishes directly when no outbox is configured.
func appendAndPublishMessage(
	ctx context.Context,
	store *store.SQL,
	eventPub *events.Publisher,
	sessionID string,
	msg wire.Message,
) error {
	if store == nil {
		return errProgressStoreRequired
	}
	batch := []wire.Message{msg}
	if err := store.AppendMessages(ctx, sessionID, batch...); err != nil {
		return err
	}
	if eventPub != nil && !store.MutationEventsOutboxed() {
		eventPub.PublishMessageAppend(ctx, projectKeyForSession(ctx, store, sessionID), sessionID, batch[0])
	}
	return nil
}

func projectKeyForSession(ctx context.Context, store *store.SQL, sessionID string) string {
	sess, err := store.Get(ctx, sessionID)
	if err != nil || sess == nil {
		return ""
	}
	return sess.ProjectID
}
