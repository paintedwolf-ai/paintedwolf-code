// Package plantest builds plan services for tests of the code that calls one.
package plantest

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/blueprint"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/internal/workerplan"
	"github.com/lycaon/lycaon/internal/workerrecords"
	"github.com/lycaon/lycaon/pkg/api"
)

// Quiet answers every collaborator question for a session with nothing
// planned: no Blueprint exists, any path is approved and ready, no workflow
// run is active, no file differs, no command ran and the scan gate is clear.
type Quiet struct{}

func (Quiet) Get(context.Context, string, string) (*api.Blueprint, error) {
	return nil, blueprint.ErrNotFound
}
func (Quiet) IsApproved(context.Context, string, string) (bool, error)             { return true, nil }
func (Quiet) ImplementWorkflowReady(context.Context, string, string) (bool, error) { return true, nil }
func (Quiet) AssertRunnable(context.Context, string) error                         { return nil }
func (Quiet) ActiveBySession(context.Context, string) (*api.WorkflowRun, error)    { return nil, nil }
func (Quiet) HeadSHA(context.Context, string) (string, error)                      { return "", nil }
func (Quiet) DiffFromRef(context.Context, string, string, string) (bool, error)    { return false, nil }
func (Quiet) HasUncommittedDiff(context.Context, string, string) (bool, error)     { return false, nil }
func (Quiet) CheckSession(context.Context, string, string) error                   { return nil }
func (Quiet) WorkerSourceRuns(context.Context, string) ([]workercompletion.WorkerInvocationReceipt, error) {
	return nil, nil
}

type noJobs struct{}

func (noJobs) Get(string) (*api.WorkerTask, bool) { return nil, false }

// Deps returns Quiet collaborators over records. A test replaces the ones
// its scenario needs.
func Deps(records *workerrecords.Store) workerplan.Deps {
	return workerplan.Deps{
		Records: records, Plans: Quiet{}, Approvals: Quiet{}, WorkflowReady: Quiet{},
		Runs: Quiet{}, ActiveRuns: Quiet{}, HeadSHA: Quiet{}, GitDiff: Quiet{}, SourceRuns: Quiet{},
		Jobs: noJobs{}, Security: Quiet{},
		HostDataDir: func(context.Context, string) (string, error) { return "", nil },
	}
}

// New builds a plan service from deps.
func New(t testing.TB, deps workerplan.Deps) *workerplan.Service {
	t.Helper()
	svc, err := workerplan.New(deps)
	if err != nil {
		t.Fatalf("plan service: %v", err)
	}
	return svc
}
