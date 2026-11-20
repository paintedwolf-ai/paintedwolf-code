package observation

import (
	"context"

	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/workeroutcomes"
)

type Sessions interface {
	ReadExecutionState(ctx context.Context, root string) (store.ExecutionState, error)
}

type Submissions interface {
	GetPromptSubmission(ctx context.Context, id string) (*store.PromptSubmission, error)
}
type Admission interface{ PromptExecutionActive(string) bool }
type Nudges interface{ HasPendingLoopWakes(string) bool }
type Turns interface {
	HostTurnBlocked(context.Context, string) bool
}

type Service struct {
	sessionStore    Sessions
	submissionStore Submissions
	admission       Admission
	nudges          Nudges
	turns           Turns
	workers         workeroutcomes.CycleLedger
	checkpoints     ExecutionCheckpointSource
}

func New(sessionStore Sessions, submissionStore Submissions, admission Admission, nudges Nudges, turns Turns) *Service {
	return &Service{sessionStore: sessionStore, submissionStore: submissionStore, admission: admission, nudges: nudges, turns: turns}
}
func (m *Service) SetWorkers(workers workeroutcomes.CycleLedger) { m.workers = workers }
