package checkpoint

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestTransactionRefUndoPreservesExternalRefs(t *testing.T) {
	for _, state := range []string{"new", "preexisting", "diverged"} {
		t.Run(state, func(t *testing.T) {
			primary := t.TempDir()
			testutil.FailErr(t, "target repository", os.MkdirAll(filepath.Join(primary, ".git/objects"), 0750))
			store, err := NewStore(t.TempDir())
			testutil.FailErr(t, "store", err)
			oid, err := WriteLooseGitObject(filepath.Join(store.RootDir(), "objects"), "blob", []byte("checkpoint object"))
			testutil.FailErr(t, "retained object", err)
			core, err := layeredfs.New(layeredfs.Config{ViewID: "refs", BackingDir: t.TempDir(), Roots: []layeredfs.RootSpec{{ID: "primary", Label: "primary", TrunkPath: primary, IsPrimary: true}}})
			testutil.FailErr(t, "view", err)
			defer func() { _ = core.ReleaseView() }()
			publisher, err := NewPublisher(PublisherConfig{Core: core, Store: store, WorkerID: "worker", TaskID: "task", ProjectID: "project"})
			testutil.FailErr(t, "publisher", err)
			snapshot := PrivateGitSnapshot{Refs: map[string]string{"refs/heads/feature": oid}}
			_, err = publisher.Publish(t.Context(), PublishRequest{CheckpointID: "checkpoint", Trigger: TriggerWorkerCompletion, PrivateGit: snapshot})
			testutil.FailErr(t, "publish", err)
			roots := map[string]string{"primary": primary}
			if state == "preexisting" {
				_, err := ImportAndValidateCheckpointGitRefs(t.Context(), store.RootDir(), "checkpoint", "worker", snapshot, roots)
				testutil.FailErr(t, "existing refs", err)
			}
			promoter, err := NewPromoter(PromoterConfig{Store: store, TargetRoots: roots})
			testutil.FailErr(t, "promoter", err)
			preview, err := promoter.Preview(t.Context(), PreviewRequest{CheckpointID: "checkpoint"})
			testutil.FailErr(t, "preview", err)
			dir := filepath.Join(t.TempDir(), "tx")
			txn, err := BeginTransaction(TransactionConfig{Dir: dir, ID: "refs"})
			testutil.FailErr(t, "begin", err)
			_, err = txn.ApplyCheckpoint(t.Context(), promoter, ApplyRequest{Preview: preview, ImportGitRefs: true})
			testutil.FailErr(t, "host apply", err)
			workerRef := "refs/lycaon/workers/worker/heads/feature"
			checkpointRef := "refs/lycaon/checkpoints/checkpoint/heads/feature"
			externalOID := ""
			if state == "diverged" {
				externalOID, err = WriteLooseGitObject(filepath.Join(primary, ".git/objects"), "blob", []byte("external object"))
				testutil.FailErr(t, "external object", err)
				testutil.FailErr(t, "external ref change", os.WriteFile(filepath.Join(primary, ".git", workerRef), []byte(externalOID+"\n"), 0600))
			}
			_, err = RecoverTransaction(t.Context(), TransactionConfig{Dir: dir})
			if state == "diverged" {
				if !errors.Is(err, ErrCheckpointRefConflict) {
					t.Fatalf("diverged ref recovery=%v", err)
				}
			} else {
				testutil.FailErr(t, "rollback refs", err)
			}
			for _, ref := range []string{workerRef, checkpointRef} {
				body, err := os.ReadFile(filepath.Join(primary, ".git", ref))
				if state == "new" {
					if !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("new ref retained %s: %v", ref, err)
					}
					continue
				}
				testutil.FailErr(t, "read preserved ref", err)
				want := oid
				if state == "diverged" && ref == workerRef {
					want = externalOID
				}
				if strings.TrimSpace(string(body)) != want {
					t.Fatalf("ref %s=%q want %s", ref, body, want)
				}
			}
		})
	}
}

// A non-primary root's refs import into that root's own repository and roll back from it.
func TestTransactionImportsAndRestoresRefsPerRoot(t *testing.T) {
	roots := map[string]string{"primary": t.TempDir(), "second": t.TempDir(), "plain": t.TempDir()}
	for _, id := range []string{"primary", "second"} {
		testutil.FailErr(t, "objects", os.MkdirAll(filepath.Join(roots[id], ".git/objects"), 0o755))
	}
	store, err := NewStore(t.TempDir())
	testutil.FailErr(t, "store", err)
	secondOID, err := WriteLooseGitObject(CheckpointGitObjectsDir(store.RootDir(), "checkpoint", "second"), "blob", []byte("second root object"))
	testutil.FailErr(t, "second object", err)
	plainOID, err := WriteLooseGitObject(CheckpointGitObjectsDir(store.RootDir(), "checkpoint", "plain"), "blob", []byte("no repository here"))
	testutil.FailErr(t, "plain object", err)
	core, err := layeredfs.New(layeredfs.Config{ViewID: "refs", BackingDir: t.TempDir(), Roots: []layeredfs.RootSpec{{ID: "primary", Label: "primary", TrunkPath: roots["primary"], IsPrimary: true}}})
	testutil.FailErr(t, "view", err)
	defer func() { _ = core.ReleaseView() }()
	publisher, err := NewPublisher(PublisherConfig{Core: core, Store: store, WorkerID: "worker", TaskID: "task", ProjectID: "project"})
	testutil.FailErr(t, "publisher", err)
	snapshot := PrivateGitSnapshot{Roots: map[string]RootGitRefs{
		"second": {HeadRef: "refs/heads/feature", Refs: map[string]string{"refs/heads/feature": secondOID}},
		"plain":  {Refs: map[string]string{"refs/heads/feature": plainOID}},
	}}
	_, err = publisher.Publish(t.Context(), PublishRequest{CheckpointID: "checkpoint", Trigger: TriggerWorkerCompletion, PrivateGit: snapshot})
	testutil.FailErr(t, "publish", err)
	promoter, err := NewPromoter(PromoterConfig{Store: store, TargetRoots: roots})
	testutil.FailErr(t, "promoter", err)
	preview, err := promoter.Preview(t.Context(), PreviewRequest{CheckpointID: "checkpoint"})
	testutil.FailErr(t, "preview", err)
	dir := filepath.Join(t.TempDir(), "tx")
	txn, err := BeginTransaction(TransactionConfig{Dir: dir, ID: "refs"})
	testutil.FailErr(t, "begin", err)
	rec, err := txn.ApplyCheckpoint(t.Context(), promoter, ApplyRequest{Preview: preview, ImportGitRefs: true})
	testutil.FailErr(t, "apply", err)
	ref := filepath.Join(roots["second"], ".git/refs/lycaon/workers/worker/heads/feature")
	body, err := os.ReadFile(ref)
	testutil.FailErr(t, "read imported ref", err)
	if strings.TrimSpace(string(body)) != secondOID {
		t.Fatalf("second root ref = %q", body)
	}
	if rec.ImportedRefs["second:refs/lycaon/workers/worker/heads/feature"] != secondOID {
		t.Fatalf("imported refs = %v", rec.ImportedRefs)
	}
	if _, err := os.Lstat(filepath.Join(roots["primary"], ".git/refs/lycaon")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a non-primary ref reached the primary root: %v", err)
	}
	if _, err := RecoverTransaction(t.Context(), TransactionConfig{Dir: dir}); err != nil {
		t.Fatalf("recover: %v", err)
	}
	if _, err := os.Lstat(ref); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rollback kept the second root's ref: %v", err)
	}
}
