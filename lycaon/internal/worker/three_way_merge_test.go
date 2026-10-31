package worker_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/promptloop"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testbaseline"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/internal/workspace"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestBranchDeltaShowsPrimaryToBranchDiff(t *testing.T) {
	delta := worker.BranchDelta("line1\nline2\n", "line1\nline2 changed\n")
	if !strings.Contains(delta, "- line2") {
		t.Fatalf("delta=%q", delta)
	}
	if !strings.Contains(delta, "+ line2 changed") {
		t.Fatalf("delta=%q", delta)
	}
}

func TestBranchDeltaEmptyWhenIdentical(t *testing.T) {
	if got := worker.BranchDelta("same\n", "same\n"); got != "" {
		t.Fatalf("delta=%q want empty", got)
	}
}

func TestExecuteFailureCodeLLMTimeout(t *testing.T) {
	err := errors.Join(promptloop.ErrLLMTurnTimeout, context.DeadlineExceeded)
	if got := worker.ExecuteFailureCode(err); got != "worker_closeout_exhausted" {
		t.Fatalf("ExecuteFailureCode = %q want worker_closeout_exhausted", got)
	}
}

func TestExecuteFailureCodeWorkspaceSetup(t *testing.T) {
	if got := worker.ExecuteFailureCode(errors.Join(worker.ErrWorkerBranchClaimFailed, workspace.ErrWorkspaceStorageExhausted)); got != "worker_workspace_disk_full" {
		t.Fatalf("storage failure code = %q want worker_workspace_disk_full", got)
	}
	if got := worker.ExecuteFailureCode(errors.Join(worker.ErrWorkerBranchClaimFailed, errors.New("permission denied"))); got != "worker_workspace_unavailable" {
		t.Fatalf("workspace failure code = %q want worker_workspace_unavailable", got)
	}
	if got := worker.ExecuteFailureCode(promptloop.ErrOwnerUnsettled); got != "worker_owner_unsettled" {
		t.Fatalf("owner unsettled code = %q want worker_owner_unsettled", got)
	}
}

func TestActiveSandboxJobIDs(t *testing.T) {
	tasks := []api.WorkerTask{
		{ID: "live-run", Status: api.WorkerStatusRunning, WorkspaceRoot: "/tmp/w1", Scope: writeScope()},
		{ID: "await-promote", Status: api.WorkerStatusComplete, WorkspaceRoot: "/tmp/w2", Scope: writeScope(), MergeStatus: api.WorkerMergeStatusPending},
		{ID: "merged", Status: api.WorkerStatusComplete, WorkspaceRoot: "/tmp/w3", Scope: writeScope(), MergeStatus: api.WorkerMergeStatusMerged},
		{ID: "failed", Status: api.WorkerStatusFailed, WorkspaceRoot: "/tmp/w4", Scope: writeScope()},
		{ID: "read-only", Status: api.WorkerStatusRunning, WorkspaceRoot: "/tmp/w5", Scope: &api.TaskScope{Mode: api.TaskScopeModeRead}},
	}
	got := worker.ActiveSandboxJobIDs(tasks)
	for _, id := range []string{"live-run", "await-promote"} {
		if _, ok := got[id]; !ok {
			t.Fatalf("ActiveSandboxJobIDs missing %q: %v", id, got)
		}
	}
	for _, id := range []string{"merged", "failed", "read-only"} {
		if _, ok := got[id]; ok {
			t.Fatalf("ActiveSandboxJobIDs should not retain %q: %v", id, got)
		}
	}
}

func writeScope() *api.TaskScope {
	return &api.TaskScope{Mode: api.TaskScopeModeWrite}
}

func TestTaskRootRefsUsesBranchSnapshotAfterProjectRename(t *testing.T) {
	ctx := t.Context()
	reg := project.NewMemoryRegistry()
	p, err := reg.Create(ctx, project.CreateParams{Roots: []project.AttachRootParams{
		{Path: t.TempDir(), Label: "app"}, {Path: t.TempDir(), Label: "docs"},
	}})
	testutil.FailErr(t, "create project", err)
	roots := project.RootRefsFrom(p)
	mgr := workspace.NewManager(filepath.Join(t.TempDir(), "branches"), filepath.Join(t.TempDir(), "seeds"))
	binding, _, err := mgr.CreateWorkerWorkspaceFromSources(ctx, roots, roots, roots[0].ID, "job-1")
	testutil.FailErr(t, "create workspace", err)
	renamed := "manuals"
	_, err = reg.PatchRoot(ctx, p.ID, roots[1].ID, project.PatchRootParams{Label: &renamed})
	testutil.FailErr(t, "rename root", err)

	refs := worker.TaskRootRefs(ctx, &api.WorkerTask{ProjectID: p.ID, WorkspaceRoot: binding.Root}, reg)
	if len(refs) != 2 || refs[1].Label != "docs" || refs[1].ID != roots[1].ID {
		t.Fatalf("refs = %+v", refs)
	}
}

func TestThreeWayMergePrimaryUnchanged(t *testing.T) {
	base := "a\nb\nc\n"
	ours := base
	theirs := "a\nB\nc\n"
	merged, hunks, clean := worker.ThreeWayMerge(base, ours, theirs)
	if !clean {
		t.Fatalf("expected clean merge hunks=%v", hunks)
	}
	if !strings.Contains(merged, "B") {
		t.Fatalf("merged=%q", merged)
	}
}

func TestThreeWayMergeConflictHunks(t *testing.T) {
	base := "a\nb\nc\n"
	ours := "a\nPRIMARY\nc\n"
	theirs := "a\nBRANCH\nc\n"
	_, hunks, clean := worker.ThreeWayMerge(base, ours, theirs)
	if clean || len(hunks) != 1 {
		t.Fatalf("clean=%v hunks=%v", clean, hunks)
	}
	if hunks[0].Primary != "PRIMARY" || hunks[0].Branch != "BRANCH" {
		t.Fatalf("hunk=%+v", hunks[0])
	}
}

func TestPromoteOverlayDoesNotInferSatisfiedEditsFromLinePresence(t *testing.T) {
	for _, tc := range []struct {
		name, base, primary, branch string
	}{
		{"indentation", "a\nb\nc\n", "a\n b\nc\n", "a\n  b\nc\n"},
		{"multiplicity", "a\nb\nc\n", "a\nb\nb\nc\n", "a\nb\nb\nb\nc\n"},
		{"ordering", "a\nb\nc\nd\n", "a\nc\nb\nd\n", "a\nd\nb\nc\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			primary, branch := t.TempDir(), t.TempDir()
			path := "content.txt"
			testutil.FailErr(t, "write baseline", os.WriteFile(filepath.Join(primary, path), []byte(tc.base), 0o644))
			baseline := workspaceBaselinePath(t, primary)
			testutil.FailErr(t, "change primary", os.WriteFile(filepath.Join(primary, path), []byte(tc.primary), 0o644))
			testutil.FailErr(t, "change worker", os.WriteFile(filepath.Join(branch, path), []byte(tc.branch), 0o644))
			task := &api.WorkerTask{
				ID: "content-identity", ParentSessionID: "parent", ProjectID: testdbseed.DefaultProjectID,
				WorkspacePath: primary, WorkspaceRoot: branch, WorkspaceBaselinePath: baseline,
				MergeStatus: api.WorkerMergeStatusPending, Status: api.WorkerStatusComplete,
				Scope: &api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{path}},
			}
			svc := &worker.MergeService{Queue: &mergeQueueStub{task: task}, Store: &mergeStoreStub{}, Reject: mergeRejectFmt(t), DataDir: t.TempDir()}
			out, err := svc.PromoteOverlay(t.Context(), "parent", task.ID, api.PromoteOverlayInput{})
			if err == nil || len(out.Conflicts) != 1 || out.OverlayPromotion != nil {
				t.Fatalf("overlapping structural edit accepted: result=%+v error=%v", out, err)
			}
			assertFileContent(t, filepath.Join(primary, path), tc.primary)
		})
	}
}

func TestAssessPromoteSafeWithPrimaryContentBaselineDespiteOverlayMtime(t *testing.T) {
	dir := t.TempDir()
	primaryPath := filepath.Join(dir, "f.go")
	branchRoot := filepath.Join(dir, settingsoverlay.DirName(), "overlays", "job-overlay")
	testutil.FailErr(t, "MkdirAll", os.MkdirAll(branchRoot, 0o755))
	content := []byte("package f\n")
	testutil.FailErr(t, "WriteFile primary", os.WriteFile(primaryPath, content, 0o644))
	past := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	testutil.FailErr(t, "Chtimes primary", os.Chtimes(primaryPath, past, past))

	branchPath := filepath.Join(branchRoot, "f.go")
	testutil.FailErr(t, "WriteFile branch", os.WriteFile(branchPath, append(content, '\n', '/'), 0o644))

	primaryBaseline := testbaseline.Capture(t, dir)

	raw := primaryBaseline
	task := &api.WorkerTask{
		ProjectID:             testdbseed.DefaultProjectID,
		WorkspacePath:         dir,
		WorkspaceRoot:         branchRoot,
		WorkspaceBaselinePath: raw,
	}
	assessment, err := worker.AssessPromotePaths3Way(context.Background(), nil, "sess", task, []string{"f.go"})
	testutil.FailErr(t, "AssessPromotePaths3Way", err)
	if len(assessment.CleanPaths) != 1 || assessment.CleanPaths[0] != "f.go" {
		t.Fatalf("expected safe merge with content baseline: %+v", assessment)
	}
}

func TestAssessPromotePaths3WayDetectsDeletedBranchFile(t *testing.T) {
	primary := t.TempDir()
	branchRoot := t.TempDir()
	baseBody := "package main\n"
	testutil.FailErr(t, "write primary", os.WriteFile(filepath.Join(primary, "main.go"), []byte(baseBody), 0o644))

	baseline := map[string]testbaseline.File{
		"main.go": {MtimeNano: 1, Content: baseBody},
	}
	raw := testbaseline.FromFiles(t, baseline)
	testutil.FailErr(t, "write metadata", workspace.WriteJobMeta(enginepaths.MetaDirForBranchRoot(branchRoot), workspace.JobMeta{
		Roots:            []workspace.JobMetaRoot{{ID: "root", Path: primary, IsPrimary: true}},
		SnapshotComplete: true,
	}))

	task := &api.WorkerTask{
		ID:                    "job-delete",
		ParentSessionID:       "sess-1",
		ProjectID:             testdbseed.DefaultProjectID,
		WorkspacePath:         primary,
		WorkspaceRoot:         branchRoot,
		MergeStatus:           api.WorkerMergeStatusPending,
		Scope:                 &api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"main.go"}},
		WorkspaceBaselinePath: raw,
		Status:                api.WorkerStatusComplete,
	}

	got, err := worker.AssessPromotePaths3Way(context.Background(), nil, "sess-1", task, []string{"main.go"})
	testutil.FailErr(t, "assess deletion", err)
	if len(got.DeletedPaths) != 1 || got.DeletedPaths[0] != "main.go" {
		t.Fatalf("DeletedPaths=%v want [main.go]", got.DeletedPaths)
	}
}
