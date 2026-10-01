package board

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/repoinfo"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type countedRepository struct {
	progressiveRepoProvider
	calls int
}

func (p *countedRepository) Brief(ctx context.Context, root string) (*repoinfo.Brief, error) {
	p.calls++
	return p.progressiveRepoProvider.Brief(ctx, root)
}

type advancingWorkflow struct {
	phase string
	calls int
}

func (w *advancingWorkflow) GetActive(context.Context, string) (*api.WorkflowRun, error) {
	w.calls++
	return &api.WorkflowRun{ID: "run", CurrentPhase: w.phase}, nil
}
func (*advancingWorkflow) AttachRunUI(context.Context, *api.WorkflowRun) error { return nil }

func TestPromptRepositoryFactsKeepWorkflowAndReservationsLive(t *testing.T) {
	repo := &countedRepository{progressiveRepoProvider: progressiveRepoProvider{brief: &repoinfo.Brief{Materialized: true, FileCount: 1}}}
	workflow := &advancingWorkflow{phase: "orient"}
	reservations := 0
	b := &SnapshotBuilder{Repo: repo, Workflow: workflow, ActiveReservations: func(string) []api.BoardReservationEntry {
		reservations++
		return nil
	}}
	root := t.TempDir()
	builder := &InjectBuilder{SnapshotBuilder: b}
	scope := builder.WithRepositoryFacts(t.Context())
	first, err := b.Build(scope, "project", root, "session", api.BoardDetailLevelCompact, nil)
	testutil.FailErr(t, "first board", err)
	repo.brief = &repoinfo.Brief{Materialized: true, FileCount: 2}
	workflow.phase = "investigate"
	second, err := b.Build(scope, "project", root, "session", api.BoardDetailLevelCompact, nil)
	testutil.FailErr(t, "advanced board", err)
	if repo.calls != 1 || first.Repo.FileCount != 1 || second.Repo.FileCount != 1 {
		t.Fatal("same preparation did not reuse repository facts")
	}
	if workflow.calls != 2 || reservations != 2 || second.ActiveWorkflowRun.CurrentPhase != "investigate" || first.ActiveWorkflowRun.CurrentPhase != "orient" {
		t.Fatal("repository reuse froze or mutated live workflow state")
	}
	third, err := b.Build(builder.WithRepositoryFacts(t.Context()), "project", root, "session", api.BoardDetailLevelCompact, nil)
	testutil.FailErr(t, "next preparation", err)
	if repo.calls != 2 || third.Repo.FileCount != 2 {
		t.Fatal("repository facts leaked into a later preparation")
	}
	_, err = b.Build(scope, "project", t.TempDir(), "session", api.BoardDetailLevelCompact, nil)
	testutil.FailErr(t, "different workspace", err)
	if repo.calls != 3 {
		t.Fatal("different workspace reused repository facts")
	}
}
