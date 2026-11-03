package checkpoint

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestHostTransactionRecovery(t *testing.T) {
	for _, phase := range []string{"complete", "partial", "file divergence", "unrecorded child", "unknown journal field", "bad journal version", "escaping image digest", "changed root", "hard link restoration"} {
		t.Run(phase, func(t *testing.T) {
			primary := t.TempDir()
			testutil.FailErr(t, "seed original", os.WriteFile(filepath.Join(primary, "a.txt"), []byte("original\n"), 0640))
			if phase == "hard link restoration" {
				testutil.FailErr(t, "seed original hard link", os.Link(filepath.Join(primary, "a.txt"), filepath.Join(primary, "b.txt")))
			}
			core, err := layeredfs.New(layeredfs.Config{ViewID: "undo-view", BackingDir: t.TempDir(), Roots: []layeredfs.RootSpec{{ID: "primary", Label: "primary", TrunkPath: primary, IsPrimary: true}}})
			testutil.FailErr(t, "create worker view", err)
			defer func() { _ = core.ReleaseView() }()
			qp := func(rel string) layeredfs.QualifiedPath { return layeredfs.QualifiedPath{RootID: "primary", Rel: rel} }
			if phase == "hard link restoration" {
				_, err := core.Lstat(qp("b.txt"))
				testutil.FailErr(t, "observe original hard-link alias", err)
			}
			_, err = core.WriteFile(qp("a.txt"), []byte("worker\n"), 0755)
			testutil.FailErr(t, "worker edit", err)
			_, err = core.Mkdir(qp("nested"), 0750)
			testutil.FailErr(t, "worker mkdir", err)
			_, err = core.Symlink("../a.txt", qp("nested/link"))
			testutil.FailErr(t, "worker link", err)
			store, err := NewStore(t.TempDir())
			testutil.FailErr(t, "create checkpoint store", err)
			publisher, err := NewPublisher(PublisherConfig{Core: core, Store: store, WorkerID: "worker", TaskID: "task", ProjectID: "project"})
			testutil.FailErr(t, "publisher", err)
			_, err = publisher.Publish(t.Context(), PublishRequest{CheckpointID: "checkpoint", Trigger: TriggerWorkerCompletion})
			testutil.FailErr(t, "publish", err)
			config := PromoterConfig{Store: store, TargetRoots: map[string]string{"primary": primary}}
			promoter, err := NewPromoter(config)
			testutil.FailErr(t, "promoter", err)
			preview, err := promoter.Preview(t.Context(), PreviewRequest{CheckpointID: "checkpoint"})
			testutil.FailErr(t, "preview", err)
			request := ApplyRequest{Preview: preview}
			failure := errors.New("interrupted checkpoint effects")
			if phase == "partial" {
				request.FaultInjector = func(stage ApplyFaultStage) error {
					if stage == ApplyFaultMidFileApply {
						return failure
					}
					return nil
				}
			}
			_, err = NewHostPromotion(promoter).Apply(t.Context(), request)
			if phase == "partial" {
				if !errors.Is(err, failure) {
					t.Fatalf("fault = %v", err)
				}
			} else {
				testutil.FailErr(t, "apply checkpoint", err)
			}
			if phase == "file divergence" {
				testutil.FailErr(t, "external edit", os.WriteFile(filepath.Join(primary, "a.txt"), []byte("external\n"), 0600))
			}
			if phase == "unrecorded child" {
				testutil.FailErr(t, "external child", os.WriteFile(filepath.Join(primary, "nested/foreign"), []byte("external\n"), 0600))
			}
			var preservedJournal []byte
			if phase == "unknown journal field" || phase == "bad journal version" || phase == "escaping image digest" {
				path := filepath.Join(store.promotionsDir, "host-transaction.json")
				raw, err := os.ReadFile(path)
				testutil.FailErr(t, "read recovery record", err)
				var document map[string]any
				testutil.FailErr(t, "decode recovery fixture", json.Unmarshal(raw, &document))
				switch phase {
				case "unknown journal field":
					document["unknown_effect"] = true
				case "bad journal version":
					document["version"] = 99
				case "escaping image digest":
					document["targets"].([]any)[0].(map[string]any)["before"].(map[string]any)["sha256"] = "../../outside"
				}
				preservedJournal, err = json.Marshal(document)
				testutil.FailErr(t, "encode invalid fixture", err)
				testutil.FailErr(t, "write invalid fixture", os.WriteFile(path, preservedJournal, 0600))
			}
			if phase == "changed root" {
				retained := filepath.Join(t.TempDir(), "retained-root")
				testutil.FailErr(t, "retain original target root", os.Rename(primary, retained))
				testutil.FailErr(t, "replace target root", os.Mkdir(primary, 0755))
				testutil.FailErr(t, "new root content", os.WriteFile(filepath.Join(primary, "a.txt"), []byte("external\n"), 0600))
			}
			// Recovery uses persisted facts rather than the old Promoter's memory.
			reopened, err := NewPromoter(config)
			testutil.FailErr(t, "reopen promoter", err)
			err = NewHostPromotion(reopened).Rollback(t.Context())
			if preservedJournal != nil {
				if err == nil {
					t.Fatal("invalid recovery journal was accepted")
				}
				raw, readErr := os.ReadFile(filepath.Join(store.promotionsDir, "host-transaction.json"))
				testutil.FailErr(t, "read refused journal", readErr)
				if !bytes.Equal(raw, preservedJournal) {
					t.Fatal("invalid recovery journal was mutated")
				}
				body, readErr := os.ReadFile(filepath.Join(primary, "a.txt"))
				testutil.FailErr(t, "read unchanged target", readErr)
				if string(body) != "worker\n" {
					t.Fatalf("invalid journal changed target=%q", body)
				}
				return
			}
			if phase == "file divergence" || phase == "unrecorded child" || phase == "changed root" {
				if err == nil {
					t.Fatal("recovery overwrote an external change")
				}
				rel := "a.txt"
				if phase == "unrecorded child" {
					rel = "nested/foreign"
				}
				body, err := os.ReadFile(filepath.Join(primary, rel))
				testutil.FailErr(t, "read preserved external change", err)
				if string(body) != "external\n" {
					t.Fatalf("external bytes = %q", body)
				}
				return
			}
			testutil.FailErr(t, "recover checkpoint", err)
			testutil.FailErr(t, "repeat checkpoint recovery", NewHostPromotion(reopened).Rollback(t.Context()))
			body, err := os.ReadFile(filepath.Join(primary, "a.txt"))
			testutil.FailErr(t, "read original", err)
			info, err := os.Stat(filepath.Join(primary, "a.txt"))
			testutil.FailErr(t, "stat original", err)
			if string(body) != "original\n" || info.Mode().Perm() != 0640 {
				t.Fatalf("recovered bytes=%q mode=%o", body, info.Mode().Perm())
			}
			if phase == "hard link restoration" {
				alias, err := os.Stat(filepath.Join(primary, "b.txt"))
				testutil.FailErr(t, "stat restored hard link", err)
				if !os.SameFile(info, alias) {
					t.Fatal("rollback restored bytes but broke the original hard-link group")
				}
			}
			if _, err := os.Lstat(filepath.Join(primary, "nested")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("created directory survived: %v", err)
			}
			// The old component's applied record must not suppress a subsequent retry.
			preview, err = reopened.Preview(t.Context(), PreviewRequest{CheckpointID: "checkpoint"})
			testutil.FailErr(t, "preview retry", err)
			_, err = NewHostPromotion(reopened).Apply(t.Context(), ApplyRequest{Preview: preview})
			testutil.FailErr(t, "retry checkpoint", err)
			link, err := os.Readlink(filepath.Join(primary, "nested/link"))
			testutil.FailErr(t, "read retried link", err)
			if link != "../a.txt" {
				t.Fatalf("retried link=%q", link)
			}
		})
	}
}

func TestHostTransactionAllHardLinksModifiedAliasDivergenceAndReadonlyDir(t *testing.T) {
	t.Run("all in-root hard links modified preserves original inode", func(t *testing.T) {
		primary := t.TempDir()
		pathA := filepath.Join(primary, "a.txt")
		pathB := filepath.Join(primary, "sub", "b.txt")
		testutil.FailErr(t, "mkdir sub", os.MkdirAll(filepath.Dir(pathB), 0o755))
		testutil.FailErr(t, "seed a.txt", os.WriteFile(pathA, []byte("shared-v1\n"), 0o640))
		testutil.FailErr(t, "link b.txt", os.Link(pathA, pathB))

		heldFD, err := os.Open(pathA)
		testutil.FailErr(t, "open held fd", err)
		defer func() { _ = heldFD.Close() }()
		origStat, err := heldFD.Stat()
		testutil.FailErr(t, "stat held fd", err)

		core, err := layeredfs.New(layeredfs.Config{
			ViewID: "all-links-view", BackingDir: t.TempDir(),
			Roots: []layeredfs.RootSpec{{ID: "primary", Label: "primary", TrunkPath: primary, IsPrimary: true}},
		})
		testutil.FailErr(t, "new core", err)
		defer func() { _ = core.ReleaseView() }()

		qp := func(rel string) layeredfs.QualifiedPath { return layeredfs.QualifiedPath{RootID: "primary", Rel: rel} }
		_, err = core.WriteFile(qp("a.txt"), []byte("worker-a\n"), 0o644)
		testutil.FailErr(t, "edit a.txt", err)
		_, err = core.WriteFile(qp("sub/b.txt"), []byte("worker-b\n"), 0o644)
		testutil.FailErr(t, "edit sub/b.txt", err)

		store, err := NewStore(t.TempDir())
		testutil.FailErr(t, "new store", err)
		pub, err := NewPublisher(PublisherConfig{Core: core, Store: store, WorkerID: "w1", TaskID: "t1", ProjectID: "p1"})
		testutil.FailErr(t, "publisher", err)
		_, err = pub.Publish(t.Context(), PublishRequest{CheckpointID: "cp-all-links", Trigger: TriggerWorkerCompletion})
		testutil.FailErr(t, "publish", err)

		cfg := PromoterConfig{Store: store, TargetRoots: map[string]string{"primary": primary}}
		promoter, err := NewPromoter(cfg)
		testutil.FailErr(t, "promoter", err)
		preview, err := promoter.Preview(t.Context(), PreviewRequest{CheckpointID: "cp-all-links"})
		testutil.FailErr(t, "preview", err)
		_, err = NewHostPromotion(promoter).Apply(t.Context(), ApplyRequest{Preview: preview})
		testutil.FailErr(t, "apply", err)

		reopened, err := NewPromoter(cfg)
		testutil.FailErr(t, "reopen", err)
		testutil.FailErr(t, "rollback", NewHostPromotion(reopened).Rollback(t.Context()))

		infoA, err := os.Stat(pathA)
		testutil.FailErr(t, "stat restored a.txt", err)
		infoB, err := os.Stat(pathB)
		testutil.FailErr(t, "stat restored b.txt", err)
		if !os.SameFile(infoA, infoB) || !os.SameFile(infoA, origStat) {
			t.Fatal("expected restored a.txt and sub/b.txt to share the exact pre-transaction inode")
		}
	})

	t.Run("external mutation through hard-link alias refuses rollback", func(t *testing.T) {
		primary := t.TempDir()
		pathA := filepath.Join(primary, "a.txt")
		pathB := filepath.Join(primary, "b.txt")
		testutil.FailErr(t, "seed a.txt", os.WriteFile(pathA, []byte("shared-v1\n"), 0o640))
		testutil.FailErr(t, "link b.txt", os.Link(pathA, pathB))

		core, err := layeredfs.New(layeredfs.Config{
			ViewID: "alias-div-view", BackingDir: t.TempDir(),
			Roots: []layeredfs.RootSpec{{ID: "primary", Label: "primary", TrunkPath: primary, IsPrimary: true}},
		})
		testutil.FailErr(t, "new core", err)
		defer func() { _ = core.ReleaseView() }()

		_, err = core.WriteFile(layeredfs.QualifiedPath{RootID: "primary", Rel: "a.txt"}, []byte("worker-a\n"), 0o640)
		testutil.FailErr(t, "edit a.txt", err)

		store, err := NewStore(t.TempDir())
		testutil.FailErr(t, "new store", err)
		pub, err := NewPublisher(PublisherConfig{Core: core, Store: store, WorkerID: "w1", TaskID: "t1", ProjectID: "p1"})
		testutil.FailErr(t, "publisher", err)
		_, err = pub.Publish(t.Context(), PublishRequest{CheckpointID: "cp-alias-div", Trigger: TriggerWorkerCompletion})
		testutil.FailErr(t, "publish", err)

		cfg := PromoterConfig{Store: store, TargetRoots: map[string]string{"primary": primary}}
		promoter, err := NewPromoter(cfg)
		testutil.FailErr(t, "promoter", err)
		preview, err := promoter.Preview(t.Context(), PreviewRequest{CheckpointID: "cp-alias-div"})
		testutil.FailErr(t, "preview", err)
		_, err = NewHostPromotion(promoter).Apply(t.Context(), ApplyRequest{Preview: preview})
		testutil.FailErr(t, "apply", err)

		// Mutate the original inode in-place through b.txt while a.txt holds the promoted inode.
		fB, err := os.OpenFile(pathB, os.O_WRONLY|os.O_TRUNC, 0o640)
		testutil.FailErr(t, "open b.txt", err)
		_, err = fB.WriteString("external-via-alias\n")
		testutil.FailErr(t, "write b.txt", err)
		testutil.FailErr(t, "close b.txt", fB.Close())

		reopened, err := NewPromoter(cfg)
		testutil.FailErr(t, "reopen", err)
		if err := NewHostPromotion(reopened).Rollback(t.Context()); err == nil {
			t.Fatal("expected rollback to refuse when retained inode was mutated via external hard-link alias")
		}
	})

	t.Run("readonly directory recovery order and store ID validation", func(t *testing.T) {
		primary := t.TempDir()
		roDir := filepath.Join(primary, "rodir")
		roChild := filepath.Join(roDir, "child.txt")
		testutil.FailErr(t, "mkdir rodir", os.MkdirAll(roDir, 0o755))
		testutil.FailErr(t, "seed child", os.WriteFile(roChild, []byte("before\n"), 0o640))
		testutil.FailErr(t, "chmod rodir 0500", os.Chmod(roDir, 0o500))
		t.Cleanup(func() { _ = os.Chmod(roDir, 0o700) })

		core, err := layeredfs.New(layeredfs.Config{
			ViewID: "ro-dir-view", BackingDir: t.TempDir(),
			Roots: []layeredfs.RootSpec{{ID: "primary", Label: "primary", TrunkPath: primary, IsPrimary: true}},
		})
		testutil.FailErr(t, "new core", err)
		defer func() { _ = core.ReleaseView() }()

		qp := func(rel string) layeredfs.QualifiedPath { return layeredfs.QualifiedPath{RootID: "primary", Rel: rel} }
		_, err = core.Chmod(qp("rodir"), 0o755)
		testutil.FailErr(t, "chmod rodir in view", err)
		_, err = core.WriteFile(qp("rodir/child.txt"), []byte("after\n"), 0o640)
		testutil.FailErr(t, "write rodir/child.txt", err)
		_, err = core.WriteFile(qp("rodir/added.txt"), []byte("added\n"), 0o640)
		testutil.FailErr(t, "write rodir/added.txt", err)

		store, err := NewStore(t.TempDir())
		testutil.FailErr(t, "new store", err)
		if _, err := store.LoadManifest("../escape"); !errors.Is(err, ErrInvalidManifest) {
			t.Fatalf("expected ErrInvalidManifest for traversal checkpoint ID, got %v", err)
		}
		if _, err := store.VerifyBlob("../escape", -1); !errors.Is(err, ErrCorruptBlob) {
			t.Fatalf("expected ErrCorruptBlob for non-hex blob SHA, got %v", err)
		}
		if _, _, err := store.LoadPromotionRecord("../escape"); !errors.Is(err, ErrInvalidManifest) {
			t.Fatalf("expected ErrInvalidManifest for traversal tx ID, got %v", err)
		}

		pub, err := NewPublisher(PublisherConfig{Core: core, Store: store, WorkerID: "w1", TaskID: "t1", ProjectID: "p1"})
		testutil.FailErr(t, "publisher", err)
		_, err = pub.Publish(t.Context(), PublishRequest{CheckpointID: "cp-ro-dir", Trigger: TriggerWorkerCompletion})
		testutil.FailErr(t, "publish", err)

		cfg := PromoterConfig{Store: store, TargetRoots: map[string]string{"primary": primary}}
		promoter, err := NewPromoter(cfg)
		testutil.FailErr(t, "promoter", err)
		preview, err := promoter.Preview(t.Context(), PreviewRequest{CheckpointID: "cp-ro-dir"})
		testutil.FailErr(t, "preview", err)
		_, err = NewHostPromotion(promoter).Apply(t.Context(), ApplyRequest{Preview: preview})
		testutil.FailErr(t, "apply", err)

		reopened, err := NewPromoter(cfg)
		testutil.FailErr(t, "reopen", err)
		testutil.FailErr(t, "rollback readonly dir", NewHostPromotion(reopened).Rollback(t.Context()))

		dirInfo, err := os.Stat(roDir)
		testutil.FailErr(t, "stat restored rodir", err)
		if dirInfo.Mode().Perm() != 0o500 {
			t.Fatalf("restored rodir mode = %o, want 0500", dirInfo.Mode().Perm())
		}
		body, err := os.ReadFile(roChild)
		testutil.FailErr(t, "read restored roChild", err)
		if string(body) != "before\n" {
			t.Fatalf("restored child = %q, want before", body)
		}
		if _, err := os.Lstat(filepath.Join(roDir, "added.txt")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("expected added.txt removed from rodir, err = %v", err)
		}
	})
}
