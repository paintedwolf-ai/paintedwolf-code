package worker_test

import (
	"context"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testbaseline"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/internal/workspace"
	"github.com/lycaon/lycaon/pkg/api"
)

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
