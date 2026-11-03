package session_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/checkpoint"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestCheckpointPromotionUsesBoundPreviewAfterNewPublication(t *testing.T) {
	primary, overlay := t.TempDir(), t.TempDir()
	store, err := checkpoint.NewStore(filepath.Join(overlay, session.TaskCheckpointStoreSubdir))
	testutil.FailErr(t, "open checkpoint store", err)
	core, err := layeredfs.New(layeredfs.Config{
		ViewID: "view-bound-preview", BackingDir: t.TempDir(),
		Roots: []layeredfs.RootSpec{{ID: "primary", TrunkPath: primary, IsPrimary: true}},
	})
	testutil.FailErr(t, "create checkpoint core", err)
	t.Cleanup(func() { _ = core.ReleaseView() })
	pub, err := checkpoint.NewPublisher(checkpoint.PublisherConfig{
		WorkerID: "job-bound-preview", TaskID: "job-bound-preview", ProjectID: testdbseed.DefaultProjectID,
		SourceGeneration: "gen-bound-preview", Core: core, Store: store,
	})
	testutil.FailErr(t, "create checkpoint publisher", err)
	path := layeredfs.QualifiedPath{RootID: "primary", Rel: "result.txt"}
	_, err = core.WriteFile(path, []byte("reviewed\n"), 0o644)
	testutil.FailErr(t, "write reviewed result", err)
	_, err = pub.Publish(t.Context(), checkpoint.PublishRequest{CheckpointID: "cp-reviewed", Trigger: checkpoint.TriggerWorkerCompletion})
	testutil.FailErr(t, "publish reviewed checkpoint", err)
	task := &api.WorkerTask{ID: "job-bound-preview", ProjectID: testdbseed.DefaultProjectID, WorkspacePath: primary, WorkspaceRoot: overlay}
	_, manifest, promoter, ok, err := session.NewTaskCheckpointPromoter(task, nil, nil)
	testutil.FailErr(t, "prepare reviewed checkpoint", err)
	if !ok {
		t.Fatal("published checkpoint was not selected")
	}
	preview, err := promoter.Preview(t.Context(), checkpoint.PreviewRequest{CheckpointID: manifest.CheckpointID})
	testutil.FailErr(t, "preview reviewed checkpoint", err)
	session.MarkTaskCheckpointPreviewBindIntent(task.ID)
	session.MaybeBindTaskCheckpointPreview(task.ID, preview)
	t.Cleanup(func() { session.ClearTaskCheckpointPreview(task.ID) })
	_, err = core.WriteFile(path, []byte("later\n"), 0o644)
	testutil.FailErr(t, "write later result", err)
	_, err = pub.Publish(t.Context(), checkpoint.PublishRequest{CheckpointID: "cp-later", Trigger: checkpoint.TriggerWorkerCompletion})
	testutil.FailErr(t, "publish later checkpoint", err)
	_, selected, _, ok, err := session.NewTaskCheckpointPromoter(task, nil, nil)
	testutil.FailErr(t, "load bound checkpoint", err)
	if !ok || selected.CheckpointID != preview.CheckpointID {
		t.Fatalf("checkpoint changed after preview: selected=%q reviewed=%q", selected.CheckpointID, preview.CheckpointID)
	}
	testutil.FailErr(t, "commit reviewed checkpoint", session.CommitTaskCheckpointJournal(t.Context(), task, nil, nil))
	body, err := os.ReadFile(filepath.Join(primary, "result.txt"))
	testutil.FailErr(t, "read promoted result", err)
	if string(body) != "reviewed\n" {
		t.Fatalf("promoted unreviewed checkpoint bytes %q", body)
	}
}
