package checkpoint

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/gitexec"
	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestTransactionRecovery(t *testing.T) {
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
			dir := filepath.Join(t.TempDir(), "tx")
			txn, err := BeginTransaction(TransactionConfig{Dir: dir, ID: "undo"})
			testutil.FailErr(t, "begin", err)
			if phase == "partial" {
				request.FaultInjector = func(stage ApplyFaultStage) error {
					if stage == ApplyFaultMidFileApply {
						panic(errSimulatedCrash)
					}
					return nil
				}
				crashDuring(t, func() { _, _ = txn.ApplyCheckpoint(t.Context(), promoter, request) })
			} else {
				_, err = txn.ApplyCheckpoint(t.Context(), promoter, request)
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
				path := filepath.Join(dir, transactionJournalName)
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
			_, err = RecoverTransaction(t.Context(), TransactionConfig{Dir: dir})
			if preservedJournal != nil {
				if err == nil {
					t.Fatal("invalid recovery journal was accepted")
				}
				raw, readErr := os.ReadFile(filepath.Join(dir, transactionJournalName))
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
			if found, err := RecoverTransaction(t.Context(), TransactionConfig{Dir: dir}); err != nil || found {
				t.Fatalf("repeat recovery found=%v err=%v", found, err)
			}
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
			_, err = promoteAndCommit(t, reopened, ApplyRequest{Preview: preview})
			testutil.FailErr(t, "retry checkpoint", err)
			link, err := os.Readlink(filepath.Join(primary, "nested/link"))
			testutil.FailErr(t, "read retried link", err)
			if link != "../a.txt" {
				t.Fatalf("retried link=%q", link)
			}
		})
	}
}

func publishAndApplyHostCheckpoint(t *testing.T, primary, checkpointID string, mutate func(*layeredfs.Core, func(string) layeredfs.QualifiedPath)) (string, PromoterConfig) {
	t.Helper()
	core, err := layeredfs.New(layeredfs.Config{
		ViewID: checkpointID + "-view", BackingDir: t.TempDir(),
		Roots: []layeredfs.RootSpec{{ID: "primary", Label: "primary", TrunkPath: primary, IsPrimary: true}},
	})
	testutil.FailErr(t, "new core", err)
	t.Cleanup(func() { _ = core.ReleaseView() })
	qp := func(rel string) layeredfs.QualifiedPath { return layeredfs.QualifiedPath{RootID: "primary", Rel: rel} }
	mutate(core, qp)
	store, err := NewStore(t.TempDir())
	testutil.FailErr(t, "new store", err)
	pub, err := NewPublisher(PublisherConfig{Core: core, Store: store, WorkerID: "w1", TaskID: "t1", ProjectID: "p1"})
	testutil.FailErr(t, "publisher", err)
	_, err = pub.Publish(t.Context(), PublishRequest{CheckpointID: checkpointID, Trigger: TriggerWorkerCompletion})
	testutil.FailErr(t, "publish", err)
	cfg := PromoterConfig{Store: store, TargetRoots: map[string]string{"primary": primary}}
	promoter, err := NewPromoter(cfg)
	testutil.FailErr(t, "promoter", err)
	preview, err := promoter.Preview(t.Context(), PreviewRequest{CheckpointID: checkpointID})
	testutil.FailErr(t, "preview", err)
	dir := filepath.Join(t.TempDir(), "tx")
	txn, err := BeginTransaction(TransactionConfig{Dir: dir, ID: "applied"})
	testutil.FailErr(t, "begin", err)
	_, err = txn.ApplyCheckpoint(t.Context(), promoter, ApplyRequest{Preview: preview})
	testutil.FailErr(t, "apply", err)
	return dir, cfg
}

var errSimulatedCrash = errors.New("simulated crash")

// crashDuring stops fn where its fault panicked, so nothing after that point runs, as in a process death.
func crashDuring(t *testing.T, fn func()) {
	t.Helper()
	defer func() {
		if r := recover(); r != errSimulatedCrash {
			t.Fatalf("expected a simulated crash, got %v", r)
		}
	}()
	fn()
}

// promoteAndCommit runs one complete promotion through the transaction.
func promoteAndCommit(t *testing.T, promoter *Promoter, req ApplyRequest) (*PromotionJournalRecord, error) {
	t.Helper()
	txn, err := BeginTransaction(TransactionConfig{Dir: filepath.Join(t.TempDir(), "tx"), ID: "promotion"})
	testutil.FailErr(t, "begin", err)
	rec, err := txn.ApplyCheckpoint(t.Context(), promoter, req)
	if err != nil {
		return nil, err
	}
	return rec, txn.Commit()
}

func recoverDir(t *testing.T, dir string) error {
	t.Helper()
	_, err := RecoverTransaction(t.Context(), TransactionConfig{Dir: dir})
	return err
}

func TestTransactionAllHardLinksModifiedPreservesOriginalInode(t *testing.T) {
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

	dir, _ := publishAndApplyHostCheckpoint(t, primary, "cp-all-links", func(core *layeredfs.Core, qp func(string) layeredfs.QualifiedPath) {
		_, err := core.WriteFile(qp("a.txt"), []byte("worker-a\n"), 0o644)
		testutil.FailErr(t, "edit a.txt", err)
		_, err = core.WriteFile(qp("sub/b.txt"), []byte("worker-b\n"), 0o644)
		testutil.FailErr(t, "edit sub/b.txt", err)
	})

	testutil.FailErr(t, "rollback", recoverDir(t, dir))

	infoA, err := os.Stat(pathA)
	testutil.FailErr(t, "stat restored a.txt", err)
	infoB, err := os.Stat(pathB)
	testutil.FailErr(t, "stat restored b.txt", err)
	if !os.SameFile(infoA, infoB) || !os.SameFile(infoA, origStat) {
		t.Fatal("expected restored a.txt and sub/b.txt to share the exact pre-transaction inode")
	}
}

func TestTransactionAliasMutationRefusesRollback(t *testing.T) {
	primary := t.TempDir()
	pathA := filepath.Join(primary, "a.txt")
	pathB := filepath.Join(primary, "b.txt")
	testutil.FailErr(t, "seed a.txt", os.WriteFile(pathA, []byte("shared-v1\n"), 0o640))
	testutil.FailErr(t, "link b.txt", os.Link(pathA, pathB))

	dir, _ := publishAndApplyHostCheckpoint(t, primary, "cp-alias-div", func(core *layeredfs.Core, qp func(string) layeredfs.QualifiedPath) {
		_, err := core.WriteFile(qp("a.txt"), []byte("worker-a\n"), 0o640)
		testutil.FailErr(t, "edit a.txt", err)
	})

	// Mutate the original inode in-place through b.txt while a.txt holds the promoted inode.
	fB, err := os.OpenFile(pathB, os.O_WRONLY|os.O_TRUNC, 0o640)
	testutil.FailErr(t, "open b.txt", err)
	_, err = fB.WriteString("external-via-alias\n")
	testutil.FailErr(t, "write b.txt", err)
	testutil.FailErr(t, "close b.txt", fB.Close())

	if err := recoverDir(t, dir); err == nil {
		t.Fatal("expected rollback to refuse when retained inode was mutated via external hard-link alias")
	}
}

func TestTransactionPostimageSidecarsFoldOrRefuse(t *testing.T) {
	primary := t.TempDir()
	pathA := filepath.Join(primary, "a.txt")
	testutil.FailErr(t, "seed a.txt", os.WriteFile(pathA, []byte("orig-single\n"), 0o640))
	heldFD, err := os.Open(pathA)
	testutil.FailErr(t, "open held fd", err)
	defer func() { _ = heldFD.Close() }()
	origStat, err := heldFD.Stat()
	testutil.FailErr(t, "stat held fd", err)

	dir, cfg := publishAndApplyHostCheckpoint(t, primary, "cp-sidecar", func(core *layeredfs.Core, qp func(string) layeredfs.QualifiedPath) {
		_, err := core.WriteFile(qp("a.txt"), []byte("worker-single\n"), 0o640)
		testutil.FailErr(t, "edit a.txt", err)
	})
	postDir := filepath.Join(dir, "postimages")
	retDir := filepath.Join(dir, "retained")
	testutil.FailErr(t, "mkdir postimages", os.MkdirAll(postDir, 0o700))
	sidecarPath := filepath.Join(postDir, "target-000000.json")
	qpA := layeredfs.QualifiedPath{RootID: "primary", Rel: "a.txt"}

	for _, badSidecar := range []struct {
		name string
		file string
		body string
		want error
	}{
		{name: "foreign transaction", file: sidecarPath, body: `{"version":1,"transaction_id":"other","index":0,"path":{"root_id":"primary","rel":"a.txt"}}`, want: ErrInvalidManifest},
		{name: "mismatched path", file: sidecarPath, body: `{"version":1,"transaction_id":"applied","index":0,"path":{"root_id":"primary","rel":"other.txt"}}`, want: ErrInvalidManifest},
		{name: "unknown field", file: sidecarPath, body: `{"version":1,"transaction_id":"applied","index":0,"path":{"root_id":"primary","rel":"a.txt"},"extra":true}`, want: ErrInvalidManifest},
		{name: "unexpected filename", file: filepath.Join(postDir, "stale.txt"), body: `{}`, want: ErrInvalidManifest},
	} {
		testutil.FailErr(t, badSidecar.name+" write", os.WriteFile(badSidecar.file, []byte(badSidecar.body), 0o600))
		err := recoverDir(t, dir)
		_ = os.Remove(badSidecar.file)
		if !errors.Is(err, badSidecar.want) {
			t.Fatalf("%s: Rollback err = %v, want %v", badSidecar.name, err, badSidecar.want)
		}
	}

	// Foreign entry in the retained slots must be refused.
	foreignSlot := filepath.Join(retDir, "slot-999999")
	testutil.FailErr(t, "write foreign slot", os.WriteFile(foreignSlot, []byte("orig-single\n"), 0o600))
	if err := recoverDir(t, dir); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("foreign retained slot: err = %v, want ErrInvalidManifest", err)
	}
	testutil.FailErr(t, "remove foreign slot", os.Remove(foreignSlot))

	// Valid bound sidecar folds on restart and restores the exact open-FD inode.
	fA, err := os.Open(pathA)
	testutil.FailErr(t, "open promoted a.txt", err)
	curXattrs, err := readFileXattrs(fA)
	_ = fA.Close()
	testutil.FailErr(t, "read promoted xattrs", err)
	validRaw, err := json.Marshal(postimageRecord{Version: 1, TransactionID: "applied", Index: 0, Path: qpA, Xattrs: curXattrs})
	testutil.FailErr(t, "marshal valid sidecar", err)
	testutil.FailErr(t, "write valid sidecar", os.WriteFile(sidecarPath, validRaw, 0o600))

	testutil.FailErr(t, "rollback with valid sidecar", recoverDir(t, dir))
	restoredStat, err := os.Stat(pathA)
	testutil.FailErr(t, "stat restored a.txt", err)
	if !os.SameFile(restoredStat, origStat) {
		t.Fatal("expected single-link regular file rollback to restore exact open-FD inode")
	}

	// Re-apply and verify that replacing or deleting the retained slot refuses rollback.
	reopened, err := NewPromoter(cfg)
	testutil.FailErr(t, "reopen", err)
	preview, err := reopened.Preview(t.Context(), PreviewRequest{CheckpointID: "cp-sidecar"})
	testutil.FailErr(t, "preview reapply", err)
	txn, err := BeginTransaction(TransactionConfig{Dir: dir, ID: "applied"})
	testutil.FailErr(t, "begin again", err)
	_, err = txn.ApplyCheckpoint(t.Context(), reopened, ApplyRequest{Preview: preview})
	testutil.FailErr(t, "reapply", err)
	slot0 := filepath.Join(retDir, "slot-000000")
	testutil.FailErr(t, "remove slot-000000", os.Remove(slot0))
	testutil.FailErr(t, "recreate slot-000000 fresh inode", os.WriteFile(slot0, []byte("orig-single\n"), 0o640))
	if err := recoverDir(t, dir); err == nil {
		t.Fatal("expected rollback to refuse replaced retained slot with fresh inode")
	}
	testutil.FailErr(t, "delete slot-000000", os.Remove(slot0))
	if err := recoverDir(t, dir); err == nil {
		t.Fatal("expected rollback to refuse missing retained slot")
	}
}

func TestTransactionReadonlyDirectoryRecoveryAndStoreIDs(t *testing.T) {
	primary := t.TempDir()
	roDir := filepath.Join(primary, "rodir")
	roChild := filepath.Join(roDir, "child.txt")
	testutil.FailErr(t, "mkdir rodir", os.MkdirAll(roDir, 0o755))
	testutil.FailErr(t, "seed child", os.WriteFile(roChild, []byte("before\n"), 0o640))
	testutil.FailErr(t, "chmod rodir 0500", os.Chmod(roDir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(roDir, 0o700) })

	dir, cfg := publishAndApplyHostCheckpoint(t, primary, "cp-ro-dir", func(core *layeredfs.Core, qp func(string) layeredfs.QualifiedPath) {
		_, err := core.Chmod(qp("rodir"), 0o755)
		testutil.FailErr(t, "chmod rodir in view", err)
		_, err = core.WriteFile(qp("rodir/child.txt"), []byte("after\n"), 0o640)
		testutil.FailErr(t, "write rodir/child.txt", err)
		_, err = core.WriteFile(qp("rodir/added.txt"), []byte("added\n"), 0o640)
		testutil.FailErr(t, "write rodir/added.txt", err)
	})
	store := cfg.Store
	if _, err := store.LoadManifest("../escape"); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("expected ErrInvalidManifest for traversal checkpoint ID, got %v", err)
	}
	if _, err := store.VerifyBlob("../escape", -1); !errors.Is(err, ErrCorruptBlob) {
		t.Fatalf("expected ErrCorruptBlob for non-hex blob SHA, got %v", err)
	}
	if _, _, err := store.LoadPromotionRecord("../escape"); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("expected ErrInvalidManifest for traversal tx ID, got %v", err)
	}

	testutil.FailErr(t, "rollback readonly dir", recoverDir(t, dir))

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
}

// A promotion stages beside the target, where a watcher can see it, so it must read as staging.
func TestTransactionStagingIsInvisibleToObservers(t *testing.T) {
	primary := t.TempDir()
	dir := filepath.Join(t.TempDir(), "tx")
	txn, err := BeginTransaction(TransactionConfig{Dir: dir, ID: "staging"})
	testutil.FailErr(t, "begin", err)
	seen := 0
	roots := map[string]string{"primary": primary}
	testutil.FailErr(t, "add roots", txn.addRoots(roots))
	txn.targets.recordPostimage = func(_ layeredfs.QualifiedPath, file *os.File) error {
		seen++
		if !fseffect.IsStaging(primary, filepath.Base(file.Name())) {
			t.Fatalf("staged %s is visible to observers", file.Name())
		}
		return nil
	}
	root, err := txn.targets.openTargetRoot("primary")
	testutil.FailErr(t, "open root", err)
	defer func() { _ = root.Close() }()
	change := PlannedChange{Path: layeredfs.QualifiedPath{RootID: "primary", Rel: "a.txt"}, Action: ActionWriteRegular, MergedBytes: []byte("a\n")}
	testutil.FailErr(t, "write", txn.targets.writeTargetRegular(root, change))
	if seen != 1 {
		t.Fatalf("staged %d times", seen)
	}
}

func TestTransactionRollbackPackedRefDelete(t *testing.T) {
	primary := t.TempDir()
	runGitTestCmd(t, primary, "init", "-q")
	testutil.FailErr(t, "seed base.txt", os.WriteFile(filepath.Join(primary, "base.txt"), []byte("base\n"), 0o640))
	runGitTestCmd(t, primary, "add", "base.txt")
	runGitTestCmd(t, primary, "commit", "-qm", "base")

	core, err := layeredfs.New(layeredfs.Config{
		ViewID:     "packed-ref-rollback",
		BackingDir: t.TempDir(),
		Roots:      []layeredfs.RootSpec{{ID: "primary", Label: "primary", TrunkPath: primary, IsPrimary: true}},
	})
	testutil.FailErr(t, "create view", err)
	defer func() { _ = core.ReleaseView() }()

	qp := func(rel string) layeredfs.QualifiedPath { return layeredfs.QualifiedPath{RootID: "primary", Rel: rel} }
	_, err = core.WriteFile(qp("base.txt"), []byte("worker\n"), 0o640)
	testutil.FailErr(t, "worker write", err)

	store, err := NewStore(t.TempDir())
	testutil.FailErr(t, "store", err)
	blobOID, err := WriteLooseGitObject(CheckpointGitObjectsDir(store.RootDir(), "cp-packed", ""), "blob", []byte("packed-ref-blob\n"))
	testutil.FailErr(t, "write loose blob", err)

	pub, err := NewPublisher(PublisherConfig{Core: core, Store: store, WorkerID: "w-packed", TaskID: "t-packed", ProjectID: "p-packed"})
	testutil.FailErr(t, "publisher", err)
	_, err = pub.Publish(t.Context(), PublishRequest{
		CheckpointID: "cp-packed",
		Trigger:      TriggerWorkerCompletion,
		PrivateGit:   PrivateGitSnapshot{Refs: map[string]string{"refs/heads/feature": blobOID}},
	})
	testutil.FailErr(t, "publish", err)

	promoter, err := NewPromoter(PromoterConfig{Store: store, TargetRoots: map[string]string{"primary": primary}})
	testutil.FailErr(t, "promoter", err)
	preview, err := promoter.Preview(t.Context(), PreviewRequest{CheckpointID: "cp-packed"})
	testutil.FailErr(t, "preview", err)

	txDir := filepath.Join(t.TempDir(), "tx-packed")
	txn, err := BeginTransaction(TransactionConfig{Dir: txDir, ID: "tx-packed"})
	testutil.FailErr(t, "begin", err)
	_, err = txn.ApplyCheckpoint(t.Context(), promoter, ApplyRequest{Preview: preview, ImportGitRefs: true})
	testutil.FailErr(t, "apply checkpoint", err)

	// Pack all refs and prune loose refs in target repository before rollback.
	runGitTestCmd(t, primary, "pack-refs", "--all", "--prune")
	for _, ref := range []string{
		"refs/lycaon/workers/w-packed/heads/feature",
		"refs/lycaon/checkpoints/cp-packed/heads/feature",
	} {
		if got := runGitTestCmd(t, primary, "rev-parse", "--verify", ref); got != blobOID {
			t.Fatalf("expected packed ref %s = %s before rollback, got %s", ref, blobOID, got)
		}
	}

	testutil.FailErr(t, "recover packed-ref transaction", recoverDir(t, txDir))

	for _, ref := range []string{
		"refs/lycaon/workers/w-packed/heads/feature",
		"refs/lycaon/checkpoints/cp-packed/heads/feature",
	} {
		if _, code, err := gitexec.Run(t.Context(), primary, []string{"rev-parse", "--verify", "--quiet", ref}, gitexec.Opts{Profile: gitexec.ProfileHermetic}); err != nil || code == 0 {
			t.Fatalf("expected rolled-back packed ref %s to be deleted, exit=%d err=%v", ref, code, err)
		}
		if out, code, err := gitexec.Run(t.Context(), primary, []string{"show-ref", "--verify", "--quiet", ref}, gitexec.Opts{Profile: gitexec.ProfileHermetic}); err != nil || code == 0 {
			t.Fatalf("expected show-ref for rolled-back packed ref %s to fail, exit=%d out=%s err=%v", ref, code, out, err)
		}
	}
}

func TestTransactionRejectsConcurrentMutationAfterCaptureOnProductionPath(t *testing.T) {
	primary := t.TempDir()
	pathA := filepath.Join(primary, "a.txt")
	pathB := filepath.Join(primary, "b.txt")
	testutil.FailErr(t, "seed a.txt", os.WriteFile(pathA, []byte("before-a\n"), 0o640))
	testutil.FailErr(t, "seed b.txt", os.WriteFile(pathB, []byte("before-b\n"), 0o640))

	txDir := filepath.Join(t.TempDir(), "tx-concurrent")
	mutatedDuringCaptureToApply := false
	txn, err := BeginTransaction(TransactionConfig{
		Dir: txDir,
		ID:  "concurrent",
		RetainLink: func(req fseffect.HardLinkRequest) error {
			if err := fseffect.HardLink(req); err != nil {
				return err
			}
			// Simulate an external writer mutating b.txt after captureTree recorded before-images
			// and before mutateTargets runs, with Reached == nil and FaultInjector == nil.
			if !mutatedDuringCaptureToApply && filepath.Base(req.Source.Rel) == "b.txt" {
				mutatedDuringCaptureToApply = true
				return os.WriteFile(pathB, []byte("external-concurrent-write\n"), 0o640)
			}
			return nil
		},
	})
	testutil.FailErr(t, "begin", err)

	err = txn.ApplyChanges(t.Context(), map[string]string{"primary": primary}, []PlannedChange{
		{Path: layeredfs.QualifiedPath{RootID: "primary", Rel: "a.txt"}, Action: ActionWriteRegular, Mode: 0o640, MergedBytes: []byte("after-a\n")},
		{Path: layeredfs.QualifiedPath{RootID: "primary", Rel: "b.txt"}, Action: ActionWriteRegular, Mode: 0o640, MergedBytes: []byte("after-b\n")},
	})
	if !errors.Is(err, ErrStalePreviewTargetChanged) {
		t.Fatalf("expected ErrStalePreviewTargetChanged when target changed between capture and apply on production path, got %v", err)
	}
	gotB, rErr := os.ReadFile(pathB)
	testutil.FailErr(t, "read b.txt", rErr)
	if string(gotB) != "external-concurrent-write\n" {
		t.Fatalf("expected external write on b.txt to be preserved, got %q", gotB)
	}
}
