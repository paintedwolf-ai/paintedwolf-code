package checkpoint

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestHostRefUndoUsesBuiltInAuthorityAndPreservesExternalRefs(t *testing.T) {
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
			promoter, err := NewPromoter(PromoterConfig{Store: store, TargetRoots: roots, RefImporter: func(context.Context, string, string, PrivateGitSnapshot) (map[string]string, error) {
				t.Fatal("host promotion invoked opaque importer")
				return nil, nil
			}})
			testutil.FailErr(t, "promoter", err)
			preview, err := promoter.Preview(t.Context(), PreviewRequest{CheckpointID: "checkpoint"})
			testutil.FailErr(t, "preview", err)
			_, err = NewHostPromotion(promoter).Apply(t.Context(), ApplyRequest{Preview: preview, ImportGitRefs: true})
			testutil.FailErr(t, "host apply", err)
			workerRef := "refs/lycaon/workers/worker/heads/feature"
			checkpointRef := "refs/lycaon/checkpoints/checkpoint/heads/feature"
			externalOID := ""
			if state == "diverged" {
				externalOID, err = WriteLooseGitObject(filepath.Join(primary, ".git/objects"), "blob", []byte("external object"))
				testutil.FailErr(t, "external object", err)
				testutil.FailErr(t, "external ref change", os.WriteFile(filepath.Join(primary, ".git", workerRef), []byte(externalOID+"\n"), 0600))
			}
			reopened, err := NewPromoter(PromoterConfig{Store: store, TargetRoots: roots})
			testutil.FailErr(t, "reopen", err)
			err = NewHostPromotion(reopened).Rollback(t.Context())
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
