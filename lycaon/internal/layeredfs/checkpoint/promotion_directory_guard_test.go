package checkpoint

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestDirectoryDeletionRefusesUnreviewedTrunkChildren(t *testing.T) {
	for _, knownChild := range []bool{false, true} {
		for _, when := range []string{"before preview", "after preview", "none"} {
			t.Run(fmt.Sprintf("known_child_%t/%s", knownChild, when), func(t *testing.T) {
				primary := t.TempDir()
				testutil.FailErr(t, "seed directory", os.Mkdir(filepath.Join(primary, "directory"), 0755))
				if knownChild {
					testutil.FailErr(t, "seed known child", os.WriteFile(filepath.Join(primary, "directory/known.txt"), []byte("known\n"), 0600))
				}
				core, err := layeredfs.New(layeredfs.Config{ViewID: "delete-directory", BackingDir: t.TempDir(), Roots: []layeredfs.RootSpec{{ID: "primary", Label: "primary", TrunkPath: primary, IsPrimary: true}}})
				testutil.FailErr(t, "worker view", err)
				defer func() { _ = core.ReleaseView() }()
				if knownChild {
					testutil.FailErr(t, "worker removes known child", core.Unlink(layeredfs.QualifiedPath{RootID: "primary", Rel: "directory/known.txt"}, false))
				}
				testutil.FailErr(t, "worker removes directory", core.Unlink(layeredfs.QualifiedPath{RootID: "primary", Rel: "directory"}, true))
				store, err := NewStore(t.TempDir())
				testutil.FailErr(t, "store", err)
				publisher, err := NewPublisher(PublisherConfig{Core: core, Store: store, WorkerID: "worker", TaskID: "task", ProjectID: "project"})
				testutil.FailErr(t, "publisher", err)
				_, err = publisher.Publish(t.Context(), PublishRequest{CheckpointID: "delete-directory", Trigger: TriggerWorkerCompletion})
				testutil.FailErr(t, "publish", err)
				promoter, err := NewPromoter(PromoterConfig{Store: store, TargetRoots: map[string]string{"primary": primary}})
				testutil.FailErr(t, "promoter", err)
				foreign := filepath.Join(primary, "directory/foreign.txt")
				if when == "before preview" {
					testutil.FailErr(t, "new trunk child", os.WriteFile(foreign, []byte("external\n"), 0600))
				}
				preview, err := promoter.Preview(t.Context(), PreviewRequest{CheckpointID: "delete-directory"})
				testutil.FailErr(t, "preview", err)
				if when == "none" {
					if len(preview.Conflicts) != 0 {
						t.Fatalf("reviewed directory deletion conflicts=%+v", preview.Conflicts)
					}
					_, err := promoter.Apply(t.Context(), ApplyRequest{Preview: preview})
					testutil.FailErr(t, "apply reviewed deletion", err)
					if _, err := os.Lstat(filepath.Join(primary, "directory")); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("deleted directory=%v", err)
					}
					return
				}
				if when == "before preview" {
					if len(preview.Conflicts) == 0 {
						t.Fatal("deletion accepted an unreviewed trunk child")
					}
				} else {
					testutil.FailErr(t, "new trunk child", os.WriteFile(foreign, []byte("external\n"), 0600))
					if _, err := promoter.Apply(t.Context(), ApplyRequest{Preview: preview}); !errors.Is(err, ErrStalePreviewTargetChanged) {
						t.Fatalf("directory drift apply=%v", err)
					}
				}
				body, err := os.ReadFile(foreign)
				testutil.FailErr(t, "preserved trunk child", err)
				if string(body) != "external\n" {
					t.Fatalf("foreign child=%q", body)
				}
			})
		}
	}
}
