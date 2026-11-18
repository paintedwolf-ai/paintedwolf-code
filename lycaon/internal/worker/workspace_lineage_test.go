package worker

import (
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter/platform"
	sessiondecisions "github.com/lycaon/lycaon/internal/session/decisions"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workspace"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestDecisionBranchStaysAttachedButCannotBePromoted(t *testing.T) {
	canonical := t.TempDir()
	testutil.FailErr(t, "write canonical file", os.WriteFile(filepath.Join(canonical, "main.go"), []byte("package original\n"), 0o644))
	projects := project.NewMemoryRegistry()
	p, err := projects.Create(t.Context(), project.CreateParams{Roots: []project.AttachRootParams{{Path: canonical, Label: "app"}}})
	testutil.FailErr(t, "create project", err)
	queue := newTestQueue(t, 1, p.ID, "parent")
	testdbseed.InsertProjectRootWithID(t, queue.db, p.ID, p.Roots[0].ID, canonical)
	queue.Branches.SetProjectStore(projects)
	queue.Branches.SetWorkerWorkspaceManager(workspace.NewManager(filepath.Join(db.DataDir(queue.db), "worker-branches"), platform.NewSelector(platform.Config{}), nil))
	id, err := queue.Enqueue(t.Context(), api.WorkerTask{
		ProjectID: p.ID, ParentSessionID: "parent", WorkspaceRootID: p.Roots[0].ID, WorkspacePath: canonical,
		AgentType: "implementer", Prompt: "choose", Brief: "choose",
		Scope: &api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"main.go"}},
	})
	testutil.FailErr(t, "enqueue worker", err)
	_, err = queue.ClaimNext(t.Context(), ClaimRequest{ProjectID: p.ID, ExecutionTarget: api.ExecutionTargetLocal})
	testutil.FailErr(t, "claim worker", err)
	claimed, err := queue.Branches.ClaimWorkerBranch(t.Context(), id)
	testutil.FailErr(t, "claim worker branch", err)
	testutil.FailErr(t, "edit decision branch", os.WriteFile(filepath.Join(claimed.WorkspaceRoot, "main.go"), []byte("package undecided\n"), 0o644))
	testdbseed.InsertSession(t, queue.db, "child", p.ID)
	testutil.FailErr(t, "bind child", queue.SetChildSessionID(t.Context(), id, "child"))
	testutil.FailErr(t, "request decision", sessiondecisions.NewSQL(queue.db).Put(t.Context(), api.WorkerDecisionRequest{
		ChildSessionID: "child", WorkerID: id, Question: "Choose the implementation", Options: []string{"A", "B"},
	}))
	won, err := queue.Complete(t.Context(), claimed, api.WorkerResult{Status: "needs_decision"})
	testutil.FailErr(t, "complete decision turn", err)
	if !won {
		t.Fatal("decision turn completion lost its worker claim")
	}
	task, ok := queue.Get(id)
	if !ok || task == nil {
		t.Fatal("decision worker missing")
	}
	if task.Status != api.WorkerStatusHeld {
		t.Fatalf("decision worker status = %q want held", task.Status)
	}
	// A branch keeps its base either as a checkpoint or as a captured baseline.
	attached := workspace.BranchUsesCheckpoints(task.WorkspaceRoot) || task.WorkspaceBaselinePath != ""
	if task.WorkspaceRoot == "" || !workspace.BranchTreePresent(task.WorkspaceRoot) || !attached {
		t.Fatalf("decision branch detached: %+v", task)
	}
	if task.MergeStatus != "" {
		t.Fatalf("decision branch became promotable with status %q", task.MergeStatus)
	}
}
