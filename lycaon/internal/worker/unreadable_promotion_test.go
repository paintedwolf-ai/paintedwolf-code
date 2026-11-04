package worker

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testbaseline"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workspace"
	"github.com/lycaon/lycaon/pkg/api"
)

// A source entry preparation left out is absent from the baseline and the branch
// alike, so promotion must not read it as something the worker deleted.
func TestEntryLeftOutOfTheBranchIsNotAWorkerDeletion(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root reads everything")
	}
	ctx := t.Context()
	project := t.TempDir()
	locked := filepath.Join(project, "locked.txt")
	closed := filepath.Join(project, "closed")
	inner := filepath.Join(closed, "inner.txt")
	testutil.FailErr(t, "mkdir", os.Mkdir(closed, 0o755))
	testutil.FailErr(t, "write inner", os.WriteFile(inner, []byte("inner\n"), 0o600))
	testutil.FailErr(t, "write locked", os.WriteFile(locked, []byte("locked\n"), 0o600))
	testutil.FailErr(t, "write main", os.WriteFile(filepath.Join(project, "main.go"), []byte("package main\n"), 0o644))
	testutil.FailErr(t, "lock file", os.Chmod(locked, 0))
	testutil.FailErr(t, "close directory", os.Chmod(closed, 0))
	restore := func() {
		_ = os.Chmod(closed, 0o755)
		_ = os.Chmod(locked, 0o600)
	}
	t.Cleanup(restore)

	sqlDB := testdbfixture.Open(t, "store.db")
	testdbseed.InsertProjectRootWithID(t, sqlDB, testdbseed.DefaultProjectID, "root-id", project)
	dataDir := testbaseline.DataDir(t, sqlDB)
	mgr := workspace.NewManager(filepath.Join(dataDir, "worker-branches"), t.TempDir())
	q := NewSQLQueue(sqlDB, 4)
	q.SetBaselineStore(sourceledger.New(sqlDB, filepath.Join(dataDir, "source-content")).Baselines)
	q.SetWorkerWorkspaceManager(mgr)
	id, err := q.Enqueue(ctx, api.WorkerTask{
		ProjectID: testdbseed.DefaultProjectID, WorkspacePath: project, WorkspaceRootID: "root-id",
		Scope:     &api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"."}},
		AgentType: "implementer", Prompt: "edit", Brief: "edit",
	})
	testutil.FailErr(t, "enqueue", err)
	claimed, err := q.ClaimNext(ctx, ClaimRequest{ProjectID: testdbseed.DefaultProjectID, ClaimedBy: "test", ExecutionTarget: api.ExecutionTargetLocal})
	testutil.FailErr(t, "claim", err)
	claimed, err = q.ClaimWorkerBranch(ctx, claimed.ID)
	testutil.FailErr(t, "claim branch", err)
	testutil.FailErr(t, "worker edit", os.WriteFile(filepath.Join(claimed.WorkspaceRoot, "main.go"), []byte("package worker\n"), 0o644))
	committed, err := q.Complete(ctx, claimed, api.WorkerResult{Status: "complete", Summary: "done"})
	testutil.FailErr(t, "complete", err)
	if !committed {
		t.Fatal("completion was not committed")
	}
	task, ok := q.Get(id)
	if !ok {
		t.Fatal("task missing")
	}
	changed, err := session.InspectOverlayChanges(ctx, task, nil)
	testutil.FailErr(t, "changes", err)
	if len(changed) != 1 || changed[0] != "main.go" {
		t.Fatalf("changes = %v", changed)
	}
	svc := &MergeService{Queue: q, Store: q, Workspace: mgr, DataDir: t.TempDir()}
	out, err := svc.PromoteOverlay(ctx, "", id, api.PromoteOverlayInput{})
	testutil.FailErr(t, "promote", err)
	if out.Status != api.WorkerMergeStatusMerged {
		t.Fatalf("promote status = %q conflicts=%+v", out.Status, out.Conflicts)
	}
	restore()
	for path, want := range map[string]string{locked: "locked\n", inner: "inner\n", filepath.Join(project, "main.go"): "package worker\n"} {
		raw, err := os.ReadFile(path)
		testutil.FailErr(t, "read after promote", err)
		if string(raw) != want {
			t.Fatalf("%s = %q, want %q", path, raw, want)
		}
	}
}
