package worker

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestReadyResolutionArtifactDrop(t *testing.T) {
	ready := readyResolutionForConflict(artifactConflict("cache/foo.pyc"))
	if ready.Action != api.WorkerPromoteResolutionActionDrop {
		t.Fatalf("action = %q want drop", ready.Action)
	}
	if !ready.NeedsReview {
		t.Fatal("opaque worker bytes must not be discarded without a choice")
	}
	if automatic := readyResolutionsToPromote([]api.WorkerReadyResolution{ready}, true); len(automatic) != 0 {
		t.Fatalf("opaque path entered automatic promotion: %+v", automatic)
	}
	if ready.PathClass != "artifact" {
		t.Fatalf("path_class = %q want artifact", ready.PathClass)
	}
}

func TestApplyPromoteResolutionDropKeepsPrimary(t *testing.T) {
	dir := t.TempDir()
	rel := "cache/foo.pyc"
	abs := filepath.Join(dir, filepath.FromSlash(rel))
	testutil.FailErr(t, "mkdir cache", os.MkdirAll(filepath.Dir(abs), 0o755))
	testutil.FailErr(t, "write pyc", os.WriteFile(abs, []byte{0, 1}, 0o600))
	task := &api.WorkerTask{ProjectID: testdbseed.DefaultProjectID, WorkspacePath: dir, WorkspaceRoot: filepath.Join(dir, ".overlay")}
	err := applyTestPromoteResolution(task, nil, api.WorkerPromoteResolution{
		Path:   rel,
		Action: api.WorkerPromoteResolutionActionDrop,
	})
	testutil.FailErr(t, "drop", err)
	got, err := os.ReadFile(abs)
	testutil.FailErr(t, "read after drop", err)
	if !bytes.Equal(got, []byte{0, 1}) {
		t.Fatalf("primary after drop = %v", got)
	}
}
