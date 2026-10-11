package observation

import (
	"context"

	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/workeroutcomes"
)

type Sessions interface {
	GetPromptSubmission(context.Context, string) (*store.PromptSubmission, error)
	ReadExecutionState(context.Context, string) (store.ExecutionState, error)
}
type Admission interface{ PromptExecutionActive(string) bool }
type Nudges interface{ HasPendingLoopWakes(string) bool }
type Turns interface {
	HostTurnBlocked(context.Context, string) bool
}

type Service struct {
	store       Sessions
	admission   Admission
	nudges      Nudges
	turns       Turns
	workers     workeroutcomes.CycleLedger
	checkpoints ExecutionCheckpointSource
}

func New(sessions Sessions, admission Admission, nudges Nudges, turns Turns) *Service {
	return &Service{store: sessions, admission: admission, nudges: nudges, turns: turns}
}
func (m *Service) SetWorkers(workers workeroutcomes.CycleLedger) { m.workers = workers }
