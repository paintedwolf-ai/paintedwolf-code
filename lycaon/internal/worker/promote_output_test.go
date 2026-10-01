package worker_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testbaseline"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestPreviewForSessionHunksOmitsFullBodies(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	branch := filepath.Join(dir, settingsoverlay.DirName(), "overlays", "job-hunks")
	testutil.FailErr(t, "MkdirAll", os.MkdirAll(branch, 0o755))
	testutil.FailErr(t, "WriteFile", os.WriteFile(filepath.Join(branch, "d.go"), []byte("branch\n"), 0o644))
	testutil.FailErr(t, "WriteFile", os.WriteFile(filepath.Join(dir, "d.go"), []byte("primary-changed\n"), 0o644))

	task := &api.WorkerTask{
		ID:                    "job-hunks",
		ParentSessionID:       "parent-1",
		ProjectID:             testdbseed.DefaultProjectID,
		WorkspacePath:         dir,
		WorkspaceRoot:         branch,
		MergeStatus:           api.WorkerMergeStatusPending,
		Scope:                 &api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"d.go"}},
		WorkspaceBaselinePath: testbaseline.FromFiles(t, map[string]testbaseline.File{"d.go": {Content: "original\n"}}),
		Status:                api.WorkerStatusComplete,
	}
	svc := &worker.MergeService{
		Queue:  &mergeQueueStub{task: task},
		Reject: mergeRejectFmt(t),
	}
	out, err := svc.PreviewForSession(ctx, "parent-1", "job-hunks", "hunks", []string{"d.go"})
	testutil.FailErr(t, "PreviewForSession hunks", err)
	if len(out.Conflicts) != 1 {
		t.Fatalf("conflicts=%v", out.Conflicts)
	}
	c := out.Conflicts[0]
	if c.Base != "" || c.Primary != "" || c.Branch != "" {
		t.Fatalf("expected hunks-only conflict payload, got base=%q primary=%q branch=%q", c.Base, c.Primary, c.Branch)
	}
	if c.BranchDelta == "" {
		t.Fatal("expected branch_delta on hunks detail")
	}
	if len(out.PathStatus) == 0 || out.PathStatus[0].Status != api.WorkerPromotePathOutcomeConflict {
		t.Fatalf("path_status=%v", out.PathStatus)
	}
	if out.PathStatus[0].ConflictTier == "" {
		t.Fatal("expected conflict_tier on path_status")
	}
	if len(out.ConflictDigest) != 1 || out.ConflictDigest[0].BranchDelta == "" {
		t.Fatalf("conflict_digest=%v", out.ConflictDigest)
	}
}

func TestPreviewForSessionIncludesOverlayIntent(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	branch := filepath.Join(dir, settingsoverlay.DirName(), "overlays", "job-intent")
	testutil.FailErr(t, "MkdirAll", os.MkdirAll(branch, 0o755))
	testutil.FailErr(t, "WriteFile", os.WriteFile(filepath.Join(branch, "d.go"), []byte("branch\n"), 0o644))
	testutil.FailErr(t, "WriteFile", os.WriteFile(filepath.Join(dir, "d.go"), []byte("primary\n"), 0o644))

	task := &api.WorkerTask{
		ID:                    "job-intent",
		ParentSessionID:       "parent-1",
		ProjectID:             testdbseed.DefaultProjectID,
		WorkspacePath:         dir,
		WorkspaceRoot:         branch,
		MergeStatus:           api.WorkerMergeStatusPending,
		Scope:                 &api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"d.go"}},
		WorkspaceBaselinePath: testbaseline.FromFiles(t, map[string]testbaseline.File{"d.go": {Content: "base\n"}}),
		Status:                api.WorkerStatusComplete,
		Result:                &api.WorkerResult{Summary: "Add who/uname builtins"},
	}
	svc := &worker.MergeService{
		Queue:  &mergeQueueStub{task: task},
		Reject: mergeRejectFmt(t),
	}
	out, err := svc.PreviewForSession(ctx, "parent-1", "job-intent", "hunks", nil)
	testutil.FailErr(t, "PreviewForSession intent", err)
	if out.OverlayIntent == nil || out.OverlayIntent.Summary != "Add who/uname builtins" {
		t.Fatalf("overlay_intent=%v", out.OverlayIntent)
	}
	if len(out.OverlayIntent.ScopePaths) != 1 || out.OverlayIntent.ScopePaths[0] != "d.go" {
		t.Fatalf("scope_paths=%v", out.OverlayIntent.ScopePaths)
	}
}

func TestPreviewForSessionFullIncludesBodies(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	branch := filepath.Join(dir, settingsoverlay.DirName(), "overlays", "job-full")
	testutil.FailErr(t, "MkdirAll", os.MkdirAll(branch, 0o755))
	testutil.FailErr(t, "WriteFile", os.WriteFile(filepath.Join(branch, "d.go"), []byte("branch\n"), 0o644))
	testutil.FailErr(t, "WriteFile", os.WriteFile(filepath.Join(dir, "d.go"), []byte("primary-changed\n"), 0o644))

	task := &api.WorkerTask{
		ID:                    "job-full",
		ParentSessionID:       "parent-1",
		ProjectID:             testdbseed.DefaultProjectID,
		WorkspacePath:         dir,
		WorkspaceRoot:         branch,
		MergeStatus:           api.WorkerMergeStatusPending,
		Scope:                 &api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"d.go"}},
		WorkspaceBaselinePath: testbaseline.FromFiles(t, map[string]testbaseline.File{"d.go": {Content: "original\n"}}),
		Status:                api.WorkerStatusComplete,
	}
	svc := &worker.MergeService{
		Queue:  &mergeQueueStub{task: task},
		Reject: mergeRejectFmt(t),
	}
	out, err := svc.PreviewForSession(ctx, "parent-1", "job-full", "full", []string{"d.go"})
	testutil.FailErr(t, "PreviewForSession full", err)
	if len(out.Conflicts) != 1 {
		t.Fatalf("conflicts=%v", out.Conflicts)
	}
	c := out.Conflicts[0]
	if c.Primary == "" || c.Branch == "" {
		t.Fatalf("expected full bodies primary=%q branch=%q", c.Primary, c.Branch)
	}
}
