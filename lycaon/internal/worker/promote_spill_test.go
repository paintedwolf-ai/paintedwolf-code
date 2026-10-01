package worker_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testbaseline"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestPreviewForSessionWritesSpillPathOnConflict(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	branch := filepath.Join(dir, settingsoverlay.DirName(), "overlays", "job-spill")
	testutil.FailErr(t, "MkdirAll", os.MkdirAll(branch, 0o755))
	testutil.FailErr(t, "WriteFile", os.WriteFile(filepath.Join(branch, "d.go"), []byte("branch\n"), 0o644))
	testutil.FailErr(t, "WriteFile", os.WriteFile(filepath.Join(dir, "d.go"), []byte("primary-changed\n"), 0o644))

	task := &api.WorkerTask{
		ID:                    "job-spill",
		ParentSessionID:       "parent-1",
		ProjectID:             testdbseed.DefaultProjectID,
		WorkspacePath:         dir,
		WorkspaceRoot:         branch,
		MergeStatus:           api.WorkerMergeStatusPending,
		Scope:                 &api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"d.go"}},
		WorkspaceBaselinePath: testbaseline.FromFiles(t, map[string]testbaseline.File{"d.go": {Content: "original\n"}}),
		Status:                api.WorkerStatusComplete,
	}
	dataDir := t.TempDir()
	svc := &worker.MergeService{
		Queue:   &mergeQueueStub{task: task},
		Reject:  mergeRejectFmt(t),
		DataDir: dataDir,
	}
	out, err := svc.PreviewForSession(ctx, "parent-1", "job-spill", "hunks", []string{"d.go"})
	testutil.FailErr(t, "PreviewForSession", err)
	wantSpill := tooloutput.PromoteSpillRelPath("job-spill")
	if out.SpillPath != wantSpill {
		t.Fatalf("spill_path=%q want %q", out.SpillPath, wantSpill)
	}
	data, err := os.ReadFile(tooloutput.DiskPath(project.HostDataDir(dataDir, testdbseed.DefaultProjectID), wantSpill))
	testutil.FailErr(t, "read spill", err)
	var spill api.WorkerMergeResult
	testutil.FailErr(t, "unmarshal spill", json.Unmarshal(data, &spill))
	if len(spill.Conflicts) != 1 || spill.Conflicts[0].Primary == "" || spill.Conflicts[0].Branch == "" {
		t.Fatalf("spill conflicts=%+v", spill.Conflicts)
	}
}

func TestPreviewForSessionPathFilter(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	branch := filepath.Join(dir, settingsoverlay.DirName(), "overlays", "job-path")
	testutil.FailErr(t, "MkdirAll", os.MkdirAll(branch, 0o755))
	testutil.FailErr(t, "WriteFile", os.WriteFile(filepath.Join(branch, "a.go"), []byte("branch-a\n"), 0o644))
	testutil.FailErr(t, "WriteFile", os.WriteFile(filepath.Join(branch, "b.go"), []byte("branch-b\n"), 0o644))
	testutil.FailErr(t, "WriteFile", os.WriteFile(filepath.Join(dir, "a.go"), []byte("primary-a\n"), 0o644))
	testutil.FailErr(t, "WriteFile", os.WriteFile(filepath.Join(dir, "b.go"), []byte("same-b\n"), 0o644))

	task := &api.WorkerTask{
		ID:                    "job-path",
		ParentSessionID:       "parent-1",
		ProjectID:             testdbseed.DefaultProjectID,
		WorkspacePath:         dir,
		WorkspaceRoot:         branch,
		MergeStatus:           api.WorkerMergeStatusPending,
		Scope:                 &api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"a.go", "b.go"}},
		WorkspaceBaselinePath: testbaseline.FromFiles(t, map[string]testbaseline.File{"a.go": {Content: "original-a\n"}, "b.go": {Content: "same-b\n"}}),
		Status:                api.WorkerStatusComplete,
	}
	svc := &worker.MergeService{
		Queue:  &mergeQueueStub{task: task},
		Reject: mergeRejectFmt(t),
	}
	out, err := svc.PreviewForSession(ctx, "parent-1", "job-path", "hunks", []string{"a.go"})
	testutil.FailErr(t, "PreviewForSession path", err)
	if len(out.Paths) != 1 || out.Paths[0] != "a.go" {
		t.Fatalf("paths=%v", out.Paths)
	}
	if len(out.Conflicts) != 1 || out.Conflicts[0].Path != "a.go" {
		t.Fatalf("conflicts=%v", out.Conflicts)
	}
}

func TestPreviewForSessionRejectsUnchangedPath(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	branch := filepath.Join(dir, settingsoverlay.DirName(), "overlays", "job-oos")
	testutil.FailErr(t, "MkdirAll", os.MkdirAll(branch, 0o755))
	task := &api.WorkerTask{
		ID:                    "job-oos",
		ParentSessionID:       "parent-1",
		ProjectID:             testdbseed.DefaultProjectID,
		WorkspacePath:         dir,
		WorkspaceRoot:         branch,
		MergeStatus:           api.WorkerMergeStatusPending,
		Scope:                 &api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"in.go"}},
		WorkspaceBaselinePath: testbaseline.FromFiles(t, map[string]testbaseline.File{"in.go": {Content: "x\n"}}),
		Status:                api.WorkerStatusComplete,
	}
	svc := &worker.MergeService{
		Queue:  &mergeQueueStub{task: task},
		Reject: mergeRejectFmt(t),
	}
	_, err := svc.PreviewForSession(ctx, "parent-1", "job-oos", "hunks", []string{"other.go"})
	if err == nil || !strings.Contains(err.Error(), worker.OverlayPromoteNoPathsCode) {
		t.Fatalf("expected no changed paths: %v", err)
	}
}
