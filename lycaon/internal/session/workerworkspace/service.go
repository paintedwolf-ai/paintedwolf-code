package workerworkspace

import (
	"context"

	"github.com/lycaon/lycaon/internal/call"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

const TaskScopeReadMutationDeniedCode = "TASK_SCOPE_READ_MUTATION_DENIED"

type Sessions interface {
	Get(context.Context, string) (*api.Session, error)
}
type Workspace interface {
	Roots(context.Context, *api.Session) ([]projectroot.RootRef, error)
}
type Tasks interface {
	Get(string) (*api.WorkerTask, bool)
	ClaimWorkerBranch(context.Context, string) (*api.WorkerTask, error)
}
type Calls interface {
	Reserve(context.Context, string, []string, string) (*call.ReservationResult, error)
	ReleaseAll(context.Context, string, string) error
	ListActiveReservations(context.Context, string) ([]call.ActiveReservation, error)
}
type Service struct {
	WorkspaceTrackerRegistry
	WorkspaceObservationOps
	store                  Sessions
	workspace              Workspace
	tasks                  Tasks
	calls                  Calls
	events                 *events.Publisher
	Touches                *TouchLedger
	branchState            func(string) (tools.BranchWorkspace, error)
	unregisterRepoObserver func()
}

func New(sessions Sessions, workspace Workspace, branchState func(string) (tools.BranchWorkspace, error)) *Service {
	s := &Service{store: sessions, workspace: workspace, branchState: branchState, Touches: NewTouchLedger()}
	s.WorkspaceTrackerRegistry.svc = s
	s.WorkspaceObservationOps.svc = s
	s.unregisterRepoObserver = repochange.RegisterObserver(func(ctx context.Context, ev repochange.Event) {
		handleWorkspaceRepoChange(s, ctx, ev)
	})
	return s
}

func (m *Service) Close() {
	if m != nil && m.unregisterRepoObserver != nil {
		m.unregisterRepoObserver()
	}
}
func (m *Service) SetTasks(tasks Tasks)                     { m.tasks = tasks }
func (m *Service) SetPublisher(publisher *events.Publisher) { m.events = publisher }
