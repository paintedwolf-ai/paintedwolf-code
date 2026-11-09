package worker

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/claim"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testbaseline"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workspace"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestClaimNextDoesNotProvisionBranch(t *testing.T) {
	dir := t.TempDir()
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644))
	sqlDB := testdbfixture.Open(t, "store.db")
	testdbseed.InsertProjectRootWithID(t, sqlDB, testdbseed.DefaultProjectID, "root-id", dir)

	q := NewSQLQueue(sqlDB, 4)
	q.SetBaselineStore(sourceledger.New(sqlDB, filepath.Join(testbaseline.DataDir(t, sqlDB), "source-content")).Baselines)
	q.SetWorkerWorkspaceManager(workspace.NewManager(filepath.Join(testbaseline.DataDir(t, sqlDB), "worker-branches")))

	id, err := q.Enqueue(context.Background(), api.WorkerTask{
		ProjectID:       testdbseed.DefaultProjectID,
		WorkspacePath:   dir,
		WorkspaceRootID: "root-id",
		Scope:           &api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"main.go"}},
		AgentType:       "implementer",
		Prompt:          "Update main.go",
		Brief:           "Update main.go",
	})
	testutil.FailErr(t, "Enqueue", err)

	task, err := q.ClaimNext(context.Background(), ClaimRequest{
		ProjectID:       testdbseed.DefaultProjectID,
		ClaimedBy:       "test",
		ExecutionTarget: api.ExecutionTargetLocal,
	})
	testutil.FailErr(t, "ClaimNext", err)
	if task.ID != id {
		t.Fatalf("id=%q want %q", task.ID, id)
	}
	if strings.TrimSpace(task.WorkspaceRoot) != "" {
		t.Fatalf("WorkspaceRoot=%q want empty at ClaimNext", task.WorkspaceRoot)
	}
	if strings.TrimSpace(task.WorkspaceBaselinePath) != "" {
		t.Fatalf("baseline=%q want empty until branch materialization", task.WorkspaceBaselinePath)
	}
	testutil.FailErr(t, "update canonical before branch claim", os.WriteFile(filepath.Join(dir, "main.go"), []byte("package claimed\n"), 0o644))

	claimed, err := q.ClaimWorkerBranch(context.Background(), id)
	testutil.FailErr(t, "ClaimWorkerBranch", err)
	if strings.TrimSpace(claimed.WorkspaceRoot) == "" {
		t.Fatal("ClaimWorkerBranch must set WorkspaceRoot")
	}
	if strings.TrimSpace(claimed.WorkspaceBaselinePath) == "" {
		t.Fatal("ClaimWorkerBranch must bind the branch baseline")
	}
	if _, err := os.Stat(claimed.WorkspaceRoot); err != nil {
		testutil.FailErr(t, "branch dir", err)
	}
	branchBytes, err := os.ReadFile(filepath.Join(claimed.WorkspaceRoot, "main.go"))
	testutil.FailErr(t, "read branch snapshot", err)
	if string(branchBytes) != "package claimed\n" {
		t.Fatalf("branch bytes = %q", branchBytes)
	}
	testutil.FailErr(t, "update canonical after branch claim", os.WriteFile(filepath.Join(dir, "main.go"), []byte("package later\n"), 0o644))
	branchBytes, err = os.ReadFile(filepath.Join(claimed.WorkspaceRoot, "main.go"))
	testutil.FailErr(t, "read immutable branch snapshot", err)
	if string(branchBytes) != "package claimed\n" {
		t.Fatalf("branch drifted with canonical source: %q", branchBytes)
	}
	again, err := q.ClaimWorkerBranch(context.Background(), id)
	testutil.FailErr(t, "ClaimWorkerBranch idempotent", err)
	if again.WorkspaceRoot != claimed.WorkspaceRoot {
		t.Fatalf("idempotent root=%q want %q", again.WorkspaceRoot, claimed.WorkspaceRoot)
	}
}

func TestClaimWorkerBranchMountedViewSkipsBaselineTreeWalkAndExposesWorkspaceState(t *testing.T) {
	dir := t.TempDir()
	for i := range 25 {
		sub := filepath.Join(dir, "pkg", string(rune('a'+(i%5))))
		testutil.FailErr(t, "mkdir sub", os.MkdirAll(sub, 0o755))
		testutil.FailErr(t, "write file", os.WriteFile(filepath.Join(sub, "file.go"), []byte("package sub\n"), 0o644))
	}
	sqlDB := testdbfixture.Open(t, "store.db")
	testdbseed.InsertProjectRootWithID(t, sqlDB, testdbseed.DefaultProjectID, "root-id", dir)

	mountedAdp := adapter.NewMountedAdapter(adapter.MountedAdapterConfig{
		AdapterID: adapter.AdapterNFSDarwin,
		TargetOS:  runtime.GOOS,
		MountProbeFn: func(_ context.Context, _, _ string) (adapter.ReasonCode, string, error) {
			return adapter.ReasonNone, "", nil
		},
	})
	sel := adapter.NewSelector(runtime.GOOS, mountedAdp, mountedAdp, adapter.NewEagerCopyAdapter(adapter.ReasonNone))
	sel.BudgetFor = adapter.ZeroBudget
	sel.Confinement = fixtureConfinement{}
	wsMgr := workspace.NewManager(filepath.Join(testbaseline.DataDir(t, sqlDB), "worker-branches")).WithSelector(sel)

	q := NewSQLQueue(sqlDB, 4)
	q.SetBaselineStore(sourceledger.New(sqlDB, filepath.Join(testbaseline.DataDir(t, sqlDB), "source-content")).Baselines)
	q.SetWorkerWorkspaceManager(wsMgr)

	id, err := q.Enqueue(t.Context(), api.WorkerTask{
		ProjectID:       testdbseed.DefaultProjectID,
		WorkspacePath:   dir,
		WorkspaceRootID: "root-id",
		Scope:           &api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"pkg/a/file.go"}},
		AgentType:       "implementer",
		Prompt:          "Update pkg/a/file.go",
		Brief:           "Update pkg/a/file.go",
	})
	testutil.FailErr(t, "Enqueue", err)

	_, err = q.ClaimNext(t.Context(), ClaimRequest{
		ProjectID:       testdbseed.DefaultProjectID,
		ClaimedBy:       "test-mounted",
		ExecutionTarget: api.ExecutionTargetLocal,
	})
	testutil.FailErr(t, "ClaimNext", err)

	// 1. Canceled context leaves no ready workspace root and allows clean retry.
	canceledCtx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := q.ClaimWorkerBranch(canceledCtx, id); err == nil {
		t.Fatal("expected canceled ClaimWorkerBranch to fail")
	}

	// 2. Retry succeeds in O(selected roots) with 0 lower-directory ReadDir walks.
	claimed, err := q.ClaimWorkerBranch(t.Context(), id)
	testutil.FailErr(t, "ClaimWorkerBranch mounted retry", err)
	if strings.TrimSpace(claimed.WorkspaceRoot) == "" || strings.TrimSpace(claimed.WorkspaceBaselinePath) == "" {
		t.Fatalf("expected WorkspaceRoot and WorkspaceBaselinePath to be bound, got root=%q baseline=%q", claimed.WorkspaceRoot, claimed.WorkspaceBaselinePath)
	}

	cv, ok := workspace.LookupBranchClaimedView(claimed.WorkspaceRoot)
	if !ok || cv == nil || cv.Prepared == nil || cv.Prepared.Core == nil {
		t.Fatalf("expected active mounted ClaimedView for %s, got ok=%v cv=%+v", claimed.WorkspaceRoot, ok, cv)
	}
	if stats := cv.Prepared.Core.Stats(); stats.LowerDirReads != 0 {
		t.Fatalf("expected 0 lower ReadDir walks across mounted ClaimWorkerBranch + baseline bind, got %d", stats.LowerDirReads)
	}

	meta, err := workspace.LoadJobMeta(enginepaths.MetaDirForBranchRoot(claimed.WorkspaceRoot))
	testutil.FailErr(t, "LoadJobMeta mounted worker branch", err)
	if meta.FormatVersion != workspace.JobMetaFormatV2 || meta.PreparationKind != adapter.PreparationMounted || meta.VisibilityMode != adapter.VisibilityLive || !meta.PendingGitInit {
		t.Fatalf("expected Format-v2 mounted live JobMeta, got %+v", meta)
	}

	snap, receipt, ok := WorkerClaimWorkspaceState(id)
	if !ok || snap.Readiness != claim.StateReady || snap.GitInit != claim.GitInitLazyPending || receipt.AdapterID != adapter.AdapterNFSDarwin {
		t.Fatalf("unexpected WorkerClaimWorkspaceState: ok=%v snap=%+v receipt=%+v", ok, snap, receipt)
	}
}
