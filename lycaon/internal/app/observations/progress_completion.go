package observations

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/progress"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func emitProgressCompletionFromContent(
	ctx context.Context,
	sessions SessionReader,
	transcript TranscriptWriter,
	eventPub *events.Publisher,
	progressStore progress.RunScopedStore,
	activeRun ActiveRunID,
	sessionID string,
	content string,
) {
	if sessions == nil || transcript == nil || eventPub == nil || progressStore == nil || activeRun == nil {
		return
	}
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
	written, err := appendProgressTranscript(ctx, sessions, transcript, eventPub, activeRun, progressStore, sessionID, msg)
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
	sessions SessionReader,
	transcript TranscriptWriter,
	eventPub *events.Publisher,
	sessionID string,
	msg wire.Message,
) error {
	if sessions == nil || transcript == nil {
		return errProgressStoreRequired
	}
	batch := []wire.Message{msg}
	if err := transcript.AppendMessages(ctx, sessionID, batch...); err != nil {
		return err
	}
	if eventPub != nil && !transcript.MessageEventsOutboxed() {
		eventPub.PublishMessageAppend(ctx, projectKeyForSession(ctx, sessions, sessionID), sessionID, batch[0])
	}
	return nil
}

func projectKeyForSession(ctx context.Context, sessions SessionReader, sessionID string) string {
	sess, err := sessions.Get(ctx, sessionID)
	if err != nil || sess == nil {
		return ""
	}
	return sess.ProjectID
}
