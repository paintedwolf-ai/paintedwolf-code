package workflow

import (
	"context"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/workflow/catalog"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowphases "github.com/lycaon/lycaon/internal/workflow/phases"
	workflowreview "github.com/lycaon/lycaon/internal/workflow/review"
	runstate "github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
)

type FanoutRoster interface {
	RosterFor(*api.WorkflowRun, workflowdef.Manifest) []string
}
type Fanout struct {
	Runs             runstate.RunsRepository
	Vars             *runstate.Variables
	Resolver         *catalog.Resolver
	Journal          *runstate.Journal
	Sessions         session.Store
	Phases           *workflowphases.Service
	Questions        *workflowreview.Questions
	Assignments      *workflowreview.Assignments
	Policy           FanoutRoster
	Progress         progress.RunScopedStore
	WorkerTasks      func(context.Context, string) ([]api.WorkerTask, error)
	WorkerToolBudget func(string) spawn.WorkerToolBudget
}
