package worker_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/scan/obligation"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testbaseline"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/pkg/api"
)

type landingPathsHook struct {
	paths []string
}

func (h *landingPathsHook) PrepareOverlayPromotion(_ context.Context, _ api.WorkerTask, paths, _ []string) (obligation.Plan, error) {
	h.paths = append([]string(nil), paths...)
	return obligation.Plan{}, nil
}

func (*landingPathsHook) PublishObligation(context.Context, obligation.Plan) error { return nil }

func TestPromoteOverlayDoesNotScanKeepOursResolution(t *testing.T) {
	dir := t.TempDir()
	branch := filepath.Join(dir, settingsoverlay.DirName(), "overlays", "job-keep-ours")
	testutil.FailErr(t, "mkdir overlay", os.MkdirAll(branch, 0o755))
	testutil.FailErr(t, "write overlay", os.WriteFile(filepath.Join(branch, "conflict.go"), []byte("worker\n"), 0o644))
	testutil.FailErr(t, "write primary", os.WriteFile(filepath.Join(dir, "conflict.go"), []byte("primary\n"), 0o644))

	task := &api.WorkerTask{
		ID: "job-keep-ours", ParentSessionID: "parent-1", ProjectID: testdbseed.DefaultProjectID,
		WorkspacePath: dir, WorkspaceRoot: branch, MergeStatus: api.WorkerMergeStatusPending,
		AgentType: "implementer", Status: api.WorkerStatusComplete,
		Scope:                 &api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"conflict.go"}},
		WorkspaceBaselinePath: testbaseline.FromFiles(t, map[string]testbaseline.File{"conflict.go": {Content: "original\n"}}),
	}
	hook := &landingPathsHook{}
	svc := &worker.MergeService{
		Queue: &mergeQueueStub{task: task}, Store: &mergeStoreStub{}, Reject: mergeRejectFmt(t), Scans: hook,
	}
	out, err := svc.PromoteOverlay(t.Context(), "parent-1", task.ID, api.PromoteOverlayInput{
		Resolutions: []api.WorkerPromoteResolution{{
			Path: "conflict.go", Action: api.WorkerPromoteResolutionActionKeepOurs,
		}},
	})
	testutil.FailErr(t, "PromoteOverlay", err)
	if out.Status != api.WorkerMergeStatusMerged {
		t.Fatalf("status = %q, want merged", out.Status)
	}
	if len(hook.paths) != 0 {
		t.Fatalf("scan attributed non-landed paths: %#v", hook.paths)
	}
	primary, err := os.ReadFile(filepath.Join(dir, "conflict.go"))
	testutil.FailErr(t, "read primary", err)
	if string(primary) != "primary\n" {
		t.Fatalf("primary = %q, want unchanged", primary)
	}
}
