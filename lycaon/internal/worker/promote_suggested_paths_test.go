package worker_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestPromoteOverlayLandsChangesOutsideSuggestedPaths(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	testutil.FailErr(t, "primary in.go", os.WriteFile(filepath.Join(dir, "in.go"), []byte("package main\n"), 0o644))

	branch := filepath.Join(dir, settingsoverlay.DirName(), "overlays", "job-scope")
	testutil.FailErr(t, "MkdirAll", os.MkdirAll(branch, 0o755))
	testutil.FailErr(t, "branch in.go", os.WriteFile(filepath.Join(branch, "in.go"), []byte("package main\n// edit\n"), 0o644))
	testutil.FailErr(t, "branch artifact", os.WriteFile(filepath.Join(branch, ".DS_Store"), []byte("junk"), 0o644))

	scope := api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"in.go"}}
	task := &api.WorkerTask{
		ID:                    "job-scope",
		ParentSessionID:       "parent-1",
		ProjectID:             testdbseed.DefaultProjectID,
		WorkspacePath:         dir,
		WorkspaceRoot:         branch,
		MergeStatus:           api.WorkerMergeStatusPending,
		AgentType:             "implementer",
		Scope:                 &scope,
		WorkspaceBaselinePath: workspaceBaselinePath(t, dir),
		Status:                api.WorkerStatusComplete,
	}
	svc := &worker.MergeService{
		Queue: &mergeQueueStub{task: task},

		Store:  &mergeStoreStub{},
		Reject: mergeRejectFmt(t),
	}

	out, err := svc.PromoteOverlay(ctx, "parent-1", "job-scope", api.PromoteOverlayInput{})
	testutil.FailErr(t, "PromoteOverlay", err)
	if out.Status != api.WorkerMergeStatusMerged {
		t.Fatalf("status=%q want merged", out.Status)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "in.go")); err != nil || string(got) != "package main\n// edit\n" {
		t.Fatalf("suggested-path work not landed: %q err=%v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(dir, ".DS_Store")); err != nil || string(got) != "junk" {
		t.Fatalf("change outside suggestion not landed: %q err=%v", got, err)
	}
}

func TestPromoteOverlayFolderDrop(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	testutil.FailErr(t, "mkdir pkg", os.MkdirAll(filepath.Join(dir, "pkg"), 0o755))
	testutil.FailErr(t, "primary a.go", os.WriteFile(filepath.Join(dir, "pkg", "a.go"), []byte("package pkg\n"), 0o644))

	branch := filepath.Join(dir, settingsoverlay.DirName(), "overlays", "job-drop")
	testutil.FailErr(t, "mkdir branch dep", os.MkdirAll(filepath.Join(branch, "pkg", "dep"), 0o755))
	testutil.FailErr(t, "branch a.go", os.WriteFile(filepath.Join(branch, "pkg", "a.go"), []byte("package pkg\n// edit\n"), 0o644))
	testutil.FailErr(t, "branch deped", os.WriteFile(filepath.Join(branch, "pkg", "dep", "x.go"), []byte("package dep\n"), 0o644))

	scope := api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"pkg"}}
	task := &api.WorkerTask{
		ID:                    "job-drop",
		ParentSessionID:       "parent-1",
		ProjectID:             testdbseed.DefaultProjectID,
		WorkspacePath:         dir,
		WorkspaceRoot:         branch,
		MergeStatus:           api.WorkerMergeStatusPending,
		AgentType:             "implementer",
		Scope:                 &scope,
		WorkspaceBaselinePath: workspaceBaselinePath(t, dir),
		Status:                api.WorkerStatusComplete,
	}
	svc := &worker.MergeService{
		Queue: &mergeQueueStub{task: task},

		Store:  &mergeStoreStub{},
		Reject: mergeRejectFmt(t),
	}

	out, err := svc.PromoteOverlay(ctx, "parent-1", "job-drop", api.PromoteOverlayInput{
		Resolutions: []api.WorkerPromoteResolution{
			{Path: "pkg/dep/", Action: api.WorkerPromoteResolutionActionDrop},
		},
	})
	testutil.FailErr(t, "PromoteOverlay folder drop", err)
	if out.Status != api.WorkerMergeStatusMerged {
		t.Fatalf("status=%q want merged", out.Status)
	}
	if _, err := os.Stat(filepath.Join(dir, "pkg", "a.go")); err != nil {
		t.Fatalf("changed file should land: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "pkg", "dep", "x.go")); !os.IsNotExist(err) {
		t.Fatalf("dropped subtree must not land: err=%v", err)
	}
}
