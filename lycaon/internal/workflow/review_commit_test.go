package workflow

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
)

type refusedReviewCommit struct {
	runstate.CommandReceipts
	err error
}

func (r refusedReviewCommit) CommitCommand(context.Context, *api.WorkflowRun, runstate.CommandMutation) error {
	return r.err
}

type reviewPublications struct{ count int }

func (p *reviewPublications) PublishSession(context.Context, *api.WorkflowRun) { p.count++ }

func TestReviewContractCommitFailureDoesNotPublishOrPause(t *testing.T) {
	mgr, _, blueprints, _ := testManager(t)
	setTestRegistry(t, mgr, blueprints, conditions.TestRegistryDeps())
	run := startReviewLoopRun(t.Context(), t, mgr)
	failure := errors.New("commit unavailable")
	journal := *mgr.Repairs.Journal
	journal.Commands = refusedReviewCommit{CommandReceipts: journal.Commands, err: failure}
	mgr.Repairs.Journal = &journal
	publications := &reviewPublications{}
	mgr.Repairs.Publication = publications

	err := mgr.Repairs.BlockContract(t.Context(), run.ID, errors.New("invalid review contract"))
	if !errors.Is(err, failure) {
		t.Fatalf("block contract error = %v, want commit failure", err)
	}
	persisted, err := mgr.Store.Runs.Get(t.Context(), run.ID)
	testutil.FailErr(t, "read run after failed commit", err)
	vars, err := mgr.Store.Runs.GetScaffoldVars(t.Context(), run.ID)
	testutil.FailErr(t, "read repair after failed commit", err)
	repair, err := runstate.CurrentReviewRepair(vars, run.CurrentPhase)
	testutil.FailErr(t, "decode repair after failed commit", err)
	if persisted.Status != api.WorkflowRunStatusRunning || persisted.Revision != run.Revision || repair != nil || publications.count != 0 {
		t.Fatalf("failed commit changed state: run=%+v repair=%+v publications=%d", persisted, repair, publications.count)
	}
	mgr.Repairs.Journal = mgr.Journal
	testutil.FailErr(t, "retry contract block", mgr.Repairs.BlockContract(t.Context(), run.ID, errors.New("invalid review contract")))
	persisted, err = mgr.Store.Runs.Get(t.Context(), run.ID)
	testutil.FailErr(t, "read retried run", err)
	if persisted.Status != api.WorkflowRunStatusPaused || publications.count != 1 {
		t.Fatalf("retry did not commit and publish once: run=%+v publications=%d", persisted, publications.count)
	}
}
