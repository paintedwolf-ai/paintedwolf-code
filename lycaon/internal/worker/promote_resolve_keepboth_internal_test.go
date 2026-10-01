package worker

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestKeepBothEmptyContentDoesNotEmptyFile(t *testing.T) {
	base := t.TempDir()
	primary := filepath.Join(base, "proj")
	if err := os.MkdirAll(primary, 0o755); err != nil {
		testutil.FailErr(t, "create directory", err)
	}
	const rel = "shared.go"
	original := []byte("package main\nfunc funcA(){}\n")
	if err := os.WriteFile(filepath.Join(primary, rel), original, 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	roots := []projectroot.RootRef{{ID: "p", Label: "a", Path: primary, IsPrimary: true}}
	task := &api.WorkerTask{
		ProjectID:       testdbseed.DefaultProjectID,
		WorkspacePath:   primary,
		WorkspaceRoot:   filepath.Join(base, "branch"),
		WorkspaceRootID: "p",
	}

	err := applyTestPromoteResolution(task, roots, api.WorkerPromoteResolution{
		Path:   rel,
		Action: api.WorkerPromoteResolutionActionKeepBoth,
	})
	if err == nil {
		t.Fatal("keep_both with empty content must return an error, not silently apply")
	}
	data, readErr := os.ReadFile(filepath.Join(primary, rel))
	testutil.FailErr(t, "read primary file", readErr)
	if string(data) != string(original) {
		t.Fatalf("primary file must be unchanged, got %q", data)
	}
}

func applyTestPromoteResolution(task *api.WorkerTask, roots []projectroot.RootRef, res api.WorkerPromoteResolution) error {
	if task == nil {
		return nil
	}
	promote := PromoteRootsForTask(task, roots)
	plan, mutates, err := preparePromoteResolution(promote, task, res)
	if err != nil || !mutates {
		return err
	}
	return applyTargetPromoteMutation(promote, task, plan)
}
