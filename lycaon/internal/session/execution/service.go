package execution

import (
	"context"

	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/pkg/api"
)

type Sessions interface {
	Get(ctx context.Context, id string) (*api.Session, error)
	ListBusySessionIDs(ctx context.Context, limit int) ([]string, error)
	SetSessionStatus(ctx context.Context, id string, status api.SessionStatus) error
}

type Transcript interface {
	LastAssistantMessageContent(ctx context.Context, sessionID string) (string, error)
}

type Turns interface {
	BeginTurn(ctx context.Context, in store.TurnStart) (store.TurnExecution, error)
	FinishTurn(ctx context.Context, turnID, attemptID string, status store.TurnStatus, finalOutputID, resultJSON, failure string) (store.Turn, error)
	LatestTurnStatus(ctx context.Context, sessionID string) (store.TurnStatus, error)
	RecoverTurns(ctx context.Context) ([]store.Turn, error)
	RecoverTurnsForSession(ctx context.Context, sessionID string) ([]store.Turn, error)
	SettleAbandonedTurnClocks(ctx context.Context) (int, error)
}

// Journal binds runtime turns to fenced durable attempts and terminal receipts.
type Journal struct {
	store       Turns
	turnFailure TurnFailureSink
}

func NewJournal(store Turns) *Journal { return &Journal{store: store} }
