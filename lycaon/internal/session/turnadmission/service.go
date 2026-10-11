package turnadmission

import (
	"context"

	"github.com/lycaon/lycaon/internal/promptresult"
	"github.com/lycaon/lycaon/internal/queue"
	"github.com/lycaon/lycaon/internal/session/lifecycle"
	"github.com/lycaon/lycaon/internal/session/promptinput"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/pkg/api"
)

type Sessions interface {
	Get(context.Context, string) (*api.Session, error)
	ListQueuedUserPromptSubmissions(context.Context, string) ([]store.PromptSubmission, error)
}
type Workspace interface {
	CheckReady(context.Context, string) error
}
type Runner interface {
	Run(context.Context, string, promptinput.Input) (*promptresult.Result, error)
}
type Turns interface {
	ReportFailure(context.Context, string, error) error
	HostTurnBlocked(context.Context, string) bool
}
type Settlement interface {
	Drain(context.Context, string) error
}
type Submissions interface {
	DrainPromptSubmissions(context.Context, string) error
}
type Nudges interface{ HasPendingLoopWakes(string) bool }
type Cycles interface {
	WorkerCycleIsIdle(context.Context, string) bool
}
type Service struct {
	Gate           *lifecycle.State
	Workspace      Workspace
	Runner         Runner
	Turns          Turns
	Settlement     Settlement
	Submissions    Submissions
	store          Sessions
	queue          *queue.Store
	nudges         Nudges
	cycles         Cycles
	promotionHook  promotionHook
	roundEndDrains roundEndDrains
}

func New(sessions Sessions, gate *lifecycle.State, workspace Workspace, turns Turns, pending *queue.Store) *Service {
	return &Service{store: sessions, Gate: gate, Workspace: workspace, Turns: turns, queue: pending}
}
func (s *Service) Bind(runner Runner, settlement Settlement, submissions Submissions) {
	s.Runner = runner
	s.Settlement = settlement
	s.Submissions = submissions
}
func (s *Service) SetLoop(nudges Nudges, cycles Cycles) { s.nudges, s.cycles = nudges, cycles }
func (s *Service) WaitDrains(ctx context.Context)       { s.roundEndDrains.wait(ctx) }
