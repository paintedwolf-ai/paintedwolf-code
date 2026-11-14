package checkpoint

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestPromotionRefusesTargetParentSymlinkEscape(t *testing.T) {
	for _, swapAfterPreview := range []bool{false, true} {
		name := "before preview"
		if swapAfterPreview {
			name = "after preview"
		}
		t.Run(name, func(t *testing.T) {
			trunk, outside := t.TempDir(), t.TempDir()
			testutil.FailErr(t, "make source directory", os.Mkdir(filepath.Join(trunk, "nested"), 0755))
			testutil.FailErr(t, "seed source", os.WriteFile(filepath.Join(trunk, "nested", "result.txt"), []byte("base\n"), 0644))
			testutil.FailErr(t, "seed outside canary", os.WriteFile(filepath.Join(outside, "result.txt"), []byte("base\n"), 0644))
			core, err := layeredfs.New(layeredfs.Config{ViewID: "view-confinement", BackingDir: t.TempDir(), Roots: []layeredfs.RootSpec{{ID: "primary", TrunkPath: trunk, IsPrimary: true}}})
			testutil.FailErr(t, "create core", err)
			t.Cleanup(func() { _ = core.ReleaseView() })
			_, err = core.WriteFile(layeredfs.QualifiedPath{RootID: "primary", Rel: "nested/result.txt"}, []byte("worker\n"), 0644)
			testutil.FailErr(t, "edit worker file", err)
			store, err := NewStore(t.TempDir())
			testutil.FailErr(t, "create store", err)
			publisher, err := NewPublisher(PublisherConfig{Core: core, Store: store, WorkerID: "worker-confinement", TaskID: "task-confinement", ProjectID: "project-confinement", SourceGeneration: "gen-confinement"})
			testutil.FailErr(t, "create publisher", err)
			_, err = publisher.Publish(t.Context(), PublishRequest{CheckpointID: "cp-confinement", Trigger: TriggerWorkerCompletion})
			testutil.FailErr(t, "publish checkpoint", err)
			promoter, err := NewPromoter(PromoterConfig{Store: store, TargetRoots: map[string]string{"primary": trunk}})
			testutil.FailErr(t, "create promoter", err)
			var preview *PromotionPreview
			if swapAfterPreview {
				preview, err = promoter.Preview(t.Context(), PreviewRequest{CheckpointID: "cp-confinement"})
				testutil.FailErr(t, "preview before swap", err)
			}
			testutil.FailErr(t, "remove original parent", os.RemoveAll(filepath.Join(trunk, "nested")))
			testutil.FailErr(t, "redirect parent outside root", os.Symlink(outside, filepath.Join(trunk, "nested")))
			if !swapAfterPreview {
				preview, err = promoter.Preview(t.Context(), PreviewRequest{CheckpointID: "cp-confinement"})
			}
			if err == nil {
				_, err = promoteAndCommit(t, promoter, ApplyRequest{Preview: preview})
			}
			if err == nil {
				t.Error("promotion accepted a target parent outside its root")
			}
			body, readErr := os.ReadFile(filepath.Join(outside, "result.txt"))
			testutil.FailErr(t, "read outside canary", readErr)
			if string(body) != "base\n" {
				t.Fatalf("promotion escaped its target root: outside bytes %q", body)
			}
		})
	}
}
