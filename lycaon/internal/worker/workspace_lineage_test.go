package worker

import (
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
	queue := NewInMemoryQueue(1)
	queue.SetProjectStore(projects)
	queue.SetWorkerWorkspaceManager(workspace.NewManager(filepath.Join(t.TempDir(), "branches"), filepath.Join(t.TempDir(), "seeds")))
	id, err := queue.Enqueue(t.Context(), api.WorkerTask{
		ProjectID: p.ID, ParentSessionID: "parent", WorkspaceRootID: p.Roots[0].ID, WorkspacePath: canonical,
		AgentType: "implementer", Prompt: "choose", Brief: "choose",
		Scope: &api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"main.go"}},
	})
	testutil.FailErr(t, "enqueue worker", err)
	_, err = queue.ClaimNext(t.Context(), ClaimRequest{ProjectID: p.ID, ExecutionTarget: api.ExecutionTargetLocal})
	testutil.FailErr(t, "claim worker", err)
	claimed, err := queue.ClaimWorkerBranch(t.Context(), id)
	testutil.FailErr(t, "claim worker branch", err)
	testutil.FailErr(t, "edit decision branch", os.WriteFile(filepath.Join(claimed.WorkspaceRoot, "main.go"), []byte("package undecided\n"), 0o644))
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
	if task.WorkspaceRoot == "" || task.WorkspaceBaselinePath == "" {
		t.Fatalf("decision branch detached: %+v", task)
	}
	if task.MergeStatus != "" {
		t.Fatalf("decision branch became promotable with status %q", task.MergeStatus)
	}
}
