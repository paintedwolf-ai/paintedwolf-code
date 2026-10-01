package worker_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/pkg/api"
)

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
