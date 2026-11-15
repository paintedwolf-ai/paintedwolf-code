package checkpoint

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestTransactionWithoutHardLinkSupport(t *testing.T) {
	unavailable := func(fseffect.HardLinkRequest) error { return &os.LinkError{Op: "link", Err: syscall.EXDEV} }
	apply := func(t *testing.T, primary string, checkpointID string) (*Transaction, error) {
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
		dir := filepath.Join(t.TempDir(), "tx")
		txn, err := BeginTransaction(TransactionConfig{Dir: dir, ID: "volume", RetainLink: unavailable})
		testutil.FailErr(t, "begin", err)
		_, err = txn.ApplyCheckpoint(t.Context(), promoter, ApplyRequest{Preview: preview})
		return txn, err
	}
	t.Run("single name restores from its blob", func(t *testing.T) {
		primary := t.TempDir()
		path := filepath.Join(primary, "a.txt")
		testutil.FailErr(t, "seed", os.WriteFile(path, []byte("original\n"), 0o640))
		txn, err := apply(t, primary, "cp-volume")
		testutil.FailErr(t, "apply without retention", err)
		tx, found, err := txn.loadJournal()
		testutil.FailErr(t, "load journal", err)
		if !found || tx.Targets[0].Before.RetainedSlot != "" || tx.Targets[0].Before.HardLinkGroup != "" {
			t.Fatalf("journal claims inode retention: %+v", tx.Targets)
		}
		testutil.FailErr(t, "rollback", txn.Rollback(t.Context()))
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

func TestTransactionRecoverySurvivesDeviceNumberChange(t *testing.T) {
	rewrite := func(t *testing.T, identity string) string {
		t.Helper()
		device, rest, ok := strings.Cut(identity, ":")
		if !ok {
			t.Fatalf("identity %q has no device field", identity)
		}
		return "9" + device + ":" + rest
	}
	applied := func(t *testing.T) (string, string, os.FileInfo) {
		t.Helper()
		primary := t.TempDir()
		path := filepath.Join(primary, "a.txt")
		testutil.FailErr(t, "seed", os.WriteFile(path, []byte("original\n"), 0o640))
		original, err := os.Stat(path)
		testutil.FailErr(t, "stat original", err)
		dir, _ := publishAndApplyHostCheckpoint(t, primary, "cp-device", func(core *layeredfs.Core, qp func(string) layeredfs.QualifiedPath) {
			_, err := core.WriteFile(qp("a.txt"), []byte("worker\n"), 0o640)
			testutil.FailErr(t, "edit", err)
		})
		journal := filepath.Join(dir, transactionJournalName)
		raw, err := os.ReadFile(journal)
		testutil.FailErr(t, "read journal", err)
		var tx transactionJournal
		testutil.FailErr(t, "decode journal", json.Unmarshal(raw, &tx))
		for id, root := range tx.Roots {
			root.Identity = rewrite(t, root.Identity)
			tx.Roots[id] = root
		}
		for i := range tx.Targets {
			if tx.Targets[i].Before.HardLinkGroup != "" {
				tx.Targets[i].Before.HardLinkGroup = rewrite(t, tx.Targets[i].Before.HardLinkGroup)
			}
		}
		raw, err = json.Marshal(tx)
		testutil.FailErr(t, "encode journal", err)
		testutil.FailErr(t, "write journal", os.WriteFile(journal, raw, 0o600))
		return path, dir, original
	}
	t.Run("valid recovery proceeds and restores the inode", func(t *testing.T) {
		path, dir, original := applied(t)
		testutil.FailErr(t, "rollback", recoverDir(t, dir))
		restored, err := os.Stat(path)
		testutil.FailErr(t, "stat restored", err)
		if !os.SameFile(restored, original) {
			t.Fatal("rollback lost the original inode")
		}
	})
	t.Run("replaced slot refuses", func(t *testing.T) {
		_, dir, _ := applied(t)
		slot := filepath.Join(dir, "retained", "slot-000000")
		testutil.FailErr(t, "remove slot", os.Remove(slot))
		testutil.FailErr(t, "replace slot", os.WriteFile(slot, []byte("original\n"), 0o640))
		testutil.FailErr(t, "chmod slot", os.Chmod(slot, 0o640))
		if err := recoverDir(t, dir); err == nil {
			t.Fatal("replaced slot accepted")
		}
	})
	t.Run("replaced primary refuses", func(t *testing.T) {
		path, dir, _ := applied(t)
		testutil.FailErr(t, "remove primary", os.Remove(path))
		testutil.FailErr(t, "replace primary", os.WriteFile(path, []byte("external\n"), 0o640))
		if err := recoverDir(t, dir); err == nil {
			t.Fatal("replaced primary accepted")
		}
		if raw, err := os.ReadFile(path); err != nil || string(raw) != "external\n" {
			t.Fatalf("refusal overwrote primary: %q %v", raw, err)
		}
	})
	t.Run("replaced root refuses", func(t *testing.T) {
		path, dir, _ := applied(t)
		root := filepath.Dir(path)
		testutil.FailErr(t, "move root", os.Rename(root, root+".moved"))
		testutil.FailErr(t, "replace root", os.Mkdir(root, 0o700))
		t.Cleanup(func() { _ = os.RemoveAll(root + ".moved") })
		if err := recoverDir(t, dir); err == nil {
			t.Fatal("replaced root accepted")
		}
	})
}
