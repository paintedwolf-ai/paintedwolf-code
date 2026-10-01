package worker_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestPromoteOverlayLandsNewFilesOnPrimary(t *testing.T) {
	ctx := context.Background()
	primary := t.TempDir()
	overlay := t.TempDir()
	scope := api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"src/engine/**"}}
	baseline := workspaceBaselinePath(t, primary)
	testutil.FailErr(t, "MkdirAll", os.MkdirAll(filepath.Join(overlay, "src", "engine"), 0o755))
	testutil.FailErr(t, "WriteFile", os.WriteFile(filepath.Join(overlay, "src", "engine", "game.py"), []byte("game\n"), 0o644))
	testutil.FailErr(t, "WriteFile", os.WriteFile(filepath.Join(overlay, "src", "engine", "pieces.py"), []byte("pieces\n"), 0o644))

	task := &api.WorkerTask{
		ID:                    "job-empty-primary",
		ParentSessionID:       "parent-1",
		ProjectID:             testdbseed.DefaultProjectID,
		WorkspacePath:         primary,
		WorkspaceRoot:         overlay,
		WorkspaceBaselinePath: baseline,
		MergeStatus:           api.WorkerMergeStatusPending,
		AgentType:             "implementer",
		Scope:                 &scope,
		Status:                api.WorkerStatusComplete,
	}
	store := &mergeStoreStub{}
	svc := &worker.MergeService{
		Queue: &mergeQueueStub{task: task},

		Store:  store,
		Reject: mergeRejectFmt(t),
	}

	out, err := svc.PromoteOverlay(ctx, "parent-1", "job-empty-primary", api.PromoteOverlayInput{})
	testutil.FailErr(t, "PromoteOverlay", err)
	if out.Status != api.WorkerMergeStatusMerged {
		t.Fatalf("status=%q want merged", out.Status)
	}
	for _, rel := range []string{"src/engine/game.py", "src/engine/pieces.py"} {
		got, err := os.ReadFile(filepath.Join(primary, filepath.FromSlash(rel)))
		testutil.FailErr(t, "ReadFile "+rel, err)
		if strings.TrimSpace(string(got)) == "" {
			t.Fatalf("%s not landed on primary", rel)
		}
	}
}
