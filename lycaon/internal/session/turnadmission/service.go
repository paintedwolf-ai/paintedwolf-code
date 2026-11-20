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
	Get(ctx context.Context, id string) (*api.Session, error)
}

type SubmissionsStore interface {
	ListQueuedUserPromptSubmissions(ctx context.Context, sessionID string) ([]store.PromptSubmission, error)
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
	Gate            *lifecycle.State
	Workspace       Workspace
	Runner          Runner
	Turns           Turns
	Settlement      Settlement
	Submissions     Submissions
	sessionStore    Sessions
	submissionStore SubmissionsStore
	queue           *queue.Store
	nudges          Nudges
	cycles          Cycles
	promotionHook   promotionHook
	roundEndDrains  roundEndDrains
}

func New(sessionStore Sessions, submissionStore SubmissionsStore, gate *lifecycle.State, workspace Workspace, turns Turns, pending *queue.Store) *Service {
	return &Service{sessionStore: sessionStore, submissionStore: submissionStore, Gate: gate, Workspace: workspace, Turns: turns, queue: pending}
}
func (s *Service) Bind(runner Runner, settlement Settlement, submissions Submissions) {
	s.Runner = runner
	s.Settlement = settlement
	s.Submissions = submissions
}
func (s *Service) SetLoop(nudges Nudges, cycles Cycles) { s.nudges, s.cycles = nudges, cycles }
func (s *Service) WaitDrains(ctx context.Context)       { s.roundEndDrains.wait(ctx) }
