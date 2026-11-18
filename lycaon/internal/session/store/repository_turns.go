package store

import (
	"context"
	"time"

	"github.com/lycaon/lycaon/internal/evidence"
)

// TurnRepository persists turn execution, model output and progress records.
type TurnRepository interface {
	AdmitModelResponse(ctx context.Context, sessionID, attemptID string) error
	BeginTurn(ctx context.Context, in TurnStart) (TurnExecution, error)
	CheckpointTurn(ctx context.Context, turnID, attemptID string, phase TurnPhase, checkpointJSON string) error
	ConfigureModelLimit(ctx context.Context, sessionID string, limit int) error
	FinishTurn(ctx context.Context, turnID, attemptID string, status TurnStatus, finalOutputID, resultJSON, failure string) (Turn, error)
	LatestTurnClock(ctx context.Context, sessionID string) (TurnClock, bool, error)
	LatestTurnLoadReceipt(ctx context.Context, sessionID string) (TurnLoadReceipt, bool, error)
	LatestTurnProgress(ctx context.Context, sessionID string) (time.Time, error)
	LatestTurnStatus(ctx context.Context, sessionID string) (TurnStatus, error)
	ListPendingModelOutputProjections(ctx context.Context) ([]PendingModelOutputProjection, error)
	ListTurnLoadReceiptsForTurns(ctx context.Context, openingMessageIDs []string) ([]TurnLoadReceipt, error)
	MarkModelOutputProjected(ctx context.Context, outputID string) error
	PutTurnClock(ctx context.Context, clock TurnClock) error
	PutTurnLoadReceipt(ctx context.Context, r TurnLoadReceipt) (TurnLoadReceipt, error)
	PutTurnSourceBrief(ctx context.Context, sessionID, openingMessageID string, mint evidence.SourceChangeMint) error
	RecoverTurns(ctx context.Context) ([]Turn, error)
	RecoverTurnsForSession(ctx context.Context, sessionID string) ([]Turn, error)
	SealTurnCloseout(ctx context.Context, commit TurnCloseoutCommit) error
	SettleAbandonedTurnClocks(ctx context.Context) (int, error)
	SettleModelOutput(ctx context.Context, out ModelOutput) (ModelOutput, error)
	SettledModelOutputIteration(ctx context.Context, attemptID string) (int, error)
	TouchTurnProgress(ctx context.Context, turnID string) error
	TurnSourceBriefs(ctx context.Context, sessionID string) (map[string]string, error)
}
