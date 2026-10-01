package worker_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestPromoteOverlayPreparesEveryMutationBeforeWriting(t *testing.T) {
	ctx := context.Background()
	primary := t.TempDir()
	for _, rel := range []string{"clean.go", "conflict.go"} {
		testutil.FailErr(t, "seed "+rel, os.WriteFile(filepath.Join(primary, rel), []byte("base\n"), 0o644))
	}
	scope := api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"clean.go", "conflict.go"}}
	baseline := workspaceBaselinePath(t, primary)
	testutil.FailErr(t, "diverge conflict", os.WriteFile(filepath.Join(primary, "conflict.go"), []byte("primary\n"), 0o644))

	branch := filepath.Join(primary, settingsoverlay.DirName(), "overlays", "job-atomic")
	testutil.FailErr(t, "mkdir branch", os.MkdirAll(branch, 0o755))
	testutil.FailErr(t, "write clean branch", os.WriteFile(filepath.Join(branch, "clean.go"), []byte("clean-branch\n"), 0o644))
	testutil.FailErr(t, "write conflict branch", os.WriteFile(filepath.Join(branch, "conflict.go"), []byte("conflict-branch\n"), 0o644))

	task := &api.WorkerTask{
		ID:                    "job-atomic",
		ParentSessionID:       "parent-1",
		ProjectID:             testdbseed.DefaultProjectID,
		WorkspacePath:         primary,
		WorkspaceRoot:         branch,
		WorkspaceBaselinePath: baseline,
		MergeStatus:           api.WorkerMergeStatusPending,
		Status:                api.WorkerStatusComplete,
		Scope:                 &scope,
	}
	svc := &worker.MergeService{
		Queue:   &mergeQueueStub{task: task},
		Store:   &mergeStoreStub{},
		Reject:  mergeRejectFmt(t),
		DataDir: t.TempDir(),
	}

	_, err := svc.PromoteOverlay(ctx, "parent-1", task.ID, api.PromoteOverlayInput{
		Resolutions: []api.WorkerPromoteResolution{{
			Path: "conflict.go",
			Hunks: []api.WorkerPromoteHunkResolution{{
				StartLine: 2,
				EndLine:   2,
				Content:   "invalid\n",
			}},
		}},
	})
	if err == nil {
		t.Fatal("promote accepted an invalid resolution")
	}
	assertFileContent(t, filepath.Join(primary, "clean.go"), "base\n")
	assertFileContent(t, filepath.Join(primary, "conflict.go"), "primary\n")
}

func TestPromoteOverlayRollsBackFilesystemWhenDurableCommitFails(t *testing.T) {
	primary := t.TempDir()
	testutil.FailErr(t, "seed primary", os.WriteFile(filepath.Join(primary, "main.go"), []byte("base\n"), 0o644))
	scope := api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"main.go"}}
	baseline := workspaceBaselinePath(t, primary)
	branch := filepath.Join(primary, settingsoverlay.DirName(), "overlays", "job-rollback")
	testutil.FailErr(t, "mkdir branch", os.MkdirAll(branch, 0o755))
	testutil.FailErr(t, "write branch", os.WriteFile(filepath.Join(branch, "main.go"), []byte("worker\n"), 0o644))
	task := &api.WorkerTask{
		ID: "job-rollback", ParentSessionID: "parent-1", ProjectID: testdbseed.DefaultProjectID,
		WorkspacePath: primary, WorkspaceRoot: branch, WorkspaceBaselinePath: baseline,
		MergeStatus: api.WorkerMergeStatusPending, Status: api.WorkerStatusComplete, Scope: &scope,
	}
	store := &mergeStoreStub{commitErr: errors.New("durable promotion rejected")}
	svc := &worker.MergeService{
		Queue: &mergeQueueStub{task: task}, Store: store,
		Reject: mergeRejectFmt(t), DataDir: t.TempDir(),
	}

	out, err := svc.PromoteOverlay(t.Context(), "parent-1", task.ID, api.PromoteOverlayInput{})
	if err == nil || !strings.Contains(err.Error(), "durable promotion rejected") {
		t.Fatalf("promote error = %v", err)
	}
	if out.OverlayPromotion != nil {
		t.Fatal("rolled-back promotion emitted merged snapshots")
	}
	assertFileContent(t, filepath.Join(primary, "main.go"), "base\n")
	if !store.released || store.status != api.WorkerMergeStatusPending {
		t.Fatalf("claim cleanup = released %v status %q", store.released, store.status)
	}
}

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	testutil.FailErr(t, "read "+filepath.Base(path), err)
	if string(raw) != want {
		t.Fatalf("%s = %q, want %q", filepath.Base(path), raw, want)
	}
}

func TestPromoteOverlayConflictMenuCannotLandCleanOrResolvedSubset(t *testing.T) {
	primary := t.TempDir()
	paths := []string{"clean.txt", "first.txt", "second.txt"}
	for _, path := range paths {
		testutil.FailErr(t, "seed baseline", os.WriteFile(filepath.Join(primary, path), []byte("base\n"), 0o644))
	}
	baseline := workspaceBaselinePath(t, primary)
	branch := t.TempDir()
	for _, path := range paths {
		testutil.FailErr(t, "seed worker edit", os.WriteFile(filepath.Join(branch, path), []byte("worker\n"), 0o644))
		if path != "clean.txt" {
			testutil.FailErr(t, "diverge primary", os.WriteFile(filepath.Join(primary, path), []byte("primary\n"), 0o644))
		}
	}
	task := &api.WorkerTask{
		ID: "job-conflicts", ParentSessionID: "parent", ProjectID: testdbseed.DefaultProjectID,
		WorkspacePath: primary, WorkspaceRoot: branch, WorkspaceBaselinePath: baseline,
		MergeStatus: api.WorkerMergeStatusPending, Status: api.WorkerStatusComplete,
		Scope: &api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: paths},
	}
	svc := &worker.MergeService{Queue: &mergeQueueStub{task: task}, Store: &mergeStoreStub{}, Reject: mergeRejectFmt(t), DataDir: t.TempDir()}
	resolutions := []api.WorkerPromoteResolution{{Path: "first.txt", Content: "resolved\n"}, {Path: "second.txt", Content: "resolved\n"}}
	for count := 0; count < len(resolutions); count++ {
		out, err := svc.PromoteOverlay(t.Context(), "parent", task.ID, api.PromoteOverlayInput{Resolutions: resolutions[:count]})
		if err == nil {
			t.Fatal("unresolved conflicts must reject promotion")
		}
		if len(out.Conflicts) != len(resolutions)-count {
			t.Fatalf("conflict menu = %+v", out.Conflicts)
		}
		if out.Status == api.WorkerMergeStatusMerged || out.OverlayPromotion != nil {
			t.Fatal("incomplete conflict choices emitted promotion facts")
		}
		assertFileContent(t, filepath.Join(primary, "clean.txt"), "base\n")
		for _, path := range paths[1:] {
			assertFileContent(t, filepath.Join(primary, path), "primary\n")
		}
	}
	out, err := svc.PromoteOverlay(t.Context(), "parent", task.ID, api.PromoteOverlayInput{Resolutions: resolutions})
	testutil.FailErr(t, "resolve every conflict together", err)
	if out.Status != api.WorkerMergeStatusMerged {
		t.Fatalf("promotion status = %s", out.Status)
	}
	assertFileContent(t, filepath.Join(primary, "clean.txt"), "worker\n")
	for _, path := range paths[1:] {
		assertFileContent(t, filepath.Join(primary, path), "resolved\n")
	}
}
