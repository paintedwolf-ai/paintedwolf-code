package session

import (
	"context"
	"log/slog"

	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/pkg/api"
)

type sourceReviewCheckpointer interface {
	CreateStructuralCheckpoint(context.Context, sourceledger.StructuralCheckpointInput) (sourceledger.Checkpoint, error)
}

func (m *Manager) createUserTurnReviewCheckpoint(ctx context.Context, sessionID string, msg api.Message) {
	if !api.IsUserIntentMessage(msg) {
		return
	}
	checkpointer, ok := m.sourceLedger.(sourceReviewCheckpointer)
	if !ok {
		return
	}
	sess, err := m.store.Get(ctx, sessionID)
	if err != nil || sess == nil {
		return
	}
	turn, err := m.store.UserTurnOrdinal(ctx, sessionID)
	if err != nil {
		return
	}
	_, err = checkpointer.CreateStructuralCheckpoint(ctx, sourceledger.StructuralCheckpointInput{
		ProjectID: sess.ProjectID, Kind: sourceledger.CheckpointTurn,
		Label: "Turn start", SessionID: sessionID, Turn: turn + 1,
	})
	if err != nil {
		slog.WarnContext(ctx, "create turn review checkpoint", "session_id", sessionID, "err", err)
	}
}
