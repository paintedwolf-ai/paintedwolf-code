package execution

import (
	"context"

	"github.com/lycaon/lycaon/internal/session/store"
)

type TurnStore interface {
	LatestTurnStatus(context.Context, string) (store.TurnStatus, error)
	BeginTurn(context.Context, store.TurnStart) (store.TurnExecution, error)
	FinishTurn(context.Context, string, string, store.TurnStatus, string, string, string) (store.Turn, error)
}

// Journal binds runtime turns to fenced durable attempts and terminal receipts.
type Journal struct {
	store       TurnStore
	turnFailure TurnFailureSink
}

func NewJournal(store TurnStore) *Journal { return &Journal{store: store} }
