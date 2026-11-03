package checkpoint

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestHostPromotionWithoutHardLinkSupport(t *testing.T) {
	unavailable := func(fseffect.HardLinkRequest) error { return &os.LinkError{Op: "link", Err: syscall.EXDEV} }
	apply := func(t *testing.T, primary string, checkpointID string) (*HostPromotion, error) {
		t.Helper()
		core, err := layeredfs.New(layeredfs.Config{ViewID: checkpointID, BackingDir: t.TempDir(), Roots: []layeredfs.RootSpec{{ID: "primary", Label: "primary", TrunkPath: primary, IsPrimary: true}}})
		testutil.FailErr(t, "new core", err)
		t.Cleanup(func() { _ = core.ReleaseView() })
		_, err = core.WriteFile(layeredfs.QualifiedPath{RootID: "primary", Rel: "a.txt"}, []byte("worker\n"), 0o640)
		testutil.FailErr(t, "worker edit", err)
		store, err := NewStore(t.TempDir())
		testutil.FailErr(t, "new store", err)
		publisher, err := NewPublisher(PublisherConfig{Core: core, Store: store, WorkerID: "w1", TaskID: "t1", ProjectID: "p1"})
		testutil.FailErr(t, "publisher", err)
		_, err = publisher.Publish(t.Context(), PublishRequest{CheckpointID: checkpointID, Trigger: TriggerWorkerCompletion})
		testutil.FailErr(t, "publish", err)
		promoter, err := NewPromoter(PromoterConfig{Store: store, TargetRoots: map[string]string{"primary": primary}})
		testutil.FailErr(t, "promoter", err)
		preview, err := promoter.Preview(t.Context(), PreviewRequest{CheckpointID: checkpointID})
		testutil.FailErr(t, "preview", err)
		host := NewHostPromotion(promoter)
		host.retainLink = unavailable
		_, err = host.Apply(t.Context(), ApplyRequest{Preview: preview})
		return host, err
	}
	t.Run("single name restores from its blob", func(t *testing.T) {
		primary := t.TempDir()
		path := filepath.Join(primary, "a.txt")
		testutil.FailErr(t, "seed", os.WriteFile(path, []byte("original\n"), 0o640))
		host, err := apply(t, primary, "cp-volume")
		testutil.FailErr(t, "apply without retention", err)
		tx, found, err := host.loadHostTransaction()
		testutil.FailErr(t, "load journal", err)
		if !found || tx.Targets[0].Before.RetainedSlot != "" || tx.Targets[0].Before.HardLinkGroup != "" {
			t.Fatalf("journal claims inode retention: %+v", tx.Targets)
		}
		testutil.FailErr(t, "rollback", host.Rollback(t.Context()))
		raw, err := os.ReadFile(path)
		testutil.FailErr(t, "read restored", err)
		if string(raw) != "original\n" {
			t.Fatalf("restored = %q", raw)
		}
	})
	t.Run("alias without a surviving peer refuses before effects", func(t *testing.T) {
		primary := t.TempDir()
		path := filepath.Join(primary, "a.txt")
		testutil.FailErr(t, "seed", os.WriteFile(path, []byte("original\n"), 0o640))
		testutil.FailErr(t, "alias", os.Link(path, filepath.Join(t.TempDir(), "alias")))
		if _, err := apply(t, primary, "cp-volume-alias"); !errors.Is(err, syscall.EXDEV) {
			t.Fatalf("apply = %v, want cross-device refusal", err)
		}
		raw, err := os.ReadFile(path)
		testutil.FailErr(t, "read untouched", err)
		if string(raw) != "original\n" {
			t.Fatalf("refusal mutated target: %q", raw)
		}
	})
}
