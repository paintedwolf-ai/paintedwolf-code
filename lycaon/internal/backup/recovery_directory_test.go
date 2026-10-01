package backup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRecoveryDirectorySurvivesLiveBodyMutationAndDeletion(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, storeRelPath)
	database := testdbfixture.OpenPath(t, path)
	bodyPath := filepath.Join(root, "drafts", "retained.txt")
	testutil.FailErr(t, "create body directory", os.MkdirAll(filepath.Dir(bodyPath), 0o700))
	testutil.FailErr(t, "seed body", os.WriteFile(bodyPath, []byte("before"), 0o600))
	plan, err := db.PlanUpgrade(t.Context(), database)
	testutil.FailErr(t, "plan current baseline", err)
	testutil.FailErr(t, "capture local snapshot", CaptureUpgradeRecovery(t.Context(), CreateOpts{ConfigDir: root, DBPath: path, SQLDB: database, SchemaUserVersion: db.SchemaVersion, AppVersion: "1.0.0"}, plan, "1.0.1"))
	snapshot, record, err := LatestUpgradeRecovery(t.Context(), root)
	testutil.FailErr(t, "select local snapshot", err)
	info, err := os.Stat(snapshot)
	testutil.FailErr(t, "inspect snapshot", err)
	if !info.IsDir() || record.Inventory.Entries < 2 {
		t.Fatalf("snapshot is not a complete directory: %+v", record)
	}
	testutil.FailErr(t, "overwrite live inode", os.WriteFile(bodyPath, []byte("after!"), 0o600))
	testutil.FailErr(t, "delete live body", os.Remove(bodyPath))
	preserved, err := os.ReadFile(filepath.Join(snapshot, "drafts", "retained.txt"))
	testutil.FailErr(t, "read independent snapshot", err)
	if string(preserved) != "before" {
		t.Fatalf("snapshot changed with live inode: %q", preserved)
	}
	_, err = Stage(t.Context(), StageOpts{ConfigDir: t.TempDir(), ArchivePath: snapshot, SchemaVersion: db.SchemaVersion})
	if err == nil {
		t.Fatal("public ZIP import accepted internal directory source")
	}
	_, err = StageLatestUpgradeRecovery(t.Context(), StageOpts{ConfigDir: root, DBPath: path, SQLDB: database, SchemaVersion: db.SchemaVersion})
	testutil.FailErr(t, "stage direct snapshot", err)
	testutil.FailErr(t, "close live database", database.Close())
	testutil.FailErr(t, "apply direct snapshot", ApplyPending(root))
	restored, err := os.ReadFile(bodyPath)
	testutil.FailErr(t, "read restored body", err)
	if string(restored) != "before" {
		t.Fatalf("restored body=%q", restored)
	}
	testutil.FailErr(t, "snapshot remains independently verifiable", verifyRecoveryDirectory(t.Context(), filepath.Join(root, db.UpgradeRecoveryDirName), record))
}

func TestRecoveryDirectoryRejectsCorruptBodyBeforePublication(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, storeRelPath)
	database := testdbfixture.OpenPath(t, path)
	testutil.FailErr(t, "create durable drafts", os.MkdirAll(filepath.Join(root, "drafts"), 0o700))
	testutil.FailErr(t, "seed draft", os.WriteFile(filepath.Join(root, "drafts", "body"), []byte("good"), 0o600))
	plan, err := db.PlanUpgrade(t.Context(), database)
	testutil.FailErr(t, "plan source", err)
	testutil.FailErr(t, "capture source", CaptureUpgradeRecovery(t.Context(), CreateOpts{ConfigDir: root, DBPath: path, SQLDB: database, SchemaUserVersion: db.SchemaVersion}, plan, "1.0.1"))
	snapshot, _, err := LatestUpgradeRecovery(t.Context(), root)
	testutil.FailErr(t, "select recovery", err)
	testutil.FailErr(t, "corrupt retained body", os.WriteFile(filepath.Join(snapshot, "drafts", "body"), []byte("evil"), 0o600))
	_, err = StageLatestUpgradeRecovery(t.Context(), StageOpts{ConfigDir: root, DBPath: path, SQLDB: database, SchemaVersion: db.SchemaVersion})
	if err == nil {
		t.Fatal("corrupt recovery body was staged")
	}
	if _, err := os.Stat(PendingMarkerPath(root)); !os.IsNotExist(err) {
		t.Fatalf("bad recovery published pending restore: %v", err)
	}
}

func TestRecoveryInventoryDoesNotApplyPublicTransferCaps(t *testing.T) {
	manifest := Manifest{Files: []FileEntry{{RelPath: storeRelPath, Kind: fileKindRegular, Size: 1}, {RelPath: "drafts/large", Kind: fileKindRegular, Size: 300 << 30}}}
	inventory, err := manifestInventory(manifest)
	testutil.FailErr(t, "inventory retained installation over public limits", err)
	if inventory.PayloadBytes != (300<<30)+1 {
		t.Fatalf("payload=%d", inventory.PayloadBytes)
	}
	if err := validateCaptureManifest(manifest, []byte("{}")); err == nil {
		t.Fatal("public export lost its configured transfer limit")
	}
}

type mutateOnReadContext struct {
	context.Context
	once   sync.Once
	mutate func()
}

func (c *mutateOnReadContext) Err() error {
	c.once.Do(c.mutate)
	return c.Context.Err()
}

func TestRecoveryStreamingFallbackRefusesChangingSource(t *testing.T) {
	for _, updated := range []string{"short", "changed body!", "longer than the original body"} {
		t.Run(updated, func(t *testing.T) {
			root := t.TempDir()
			source, destination := filepath.Join(root, "source"), filepath.Join(root, "snapshot")
			testutil.FailErr(t, "write mutable source", os.WriteFile(source, []byte("original body"), 0o600))
			ctx := &mutateOnReadContext{Context: t.Context(), mutate: func() {
				testutil.FailErr(t, "change source during copy", os.WriteFile(source, []byte(updated), 0o600))
				testutil.FailErr(t, "record a changed source timestamp", os.Chtimes(source, time.Now(), time.Now().Add(time.Second)))
			}}
			err := copySnapshotStream(ctx, source, destination, 0o600)
			if !errors.Is(err, ErrCaptureIncomplete) {
				t.Fatalf("changing source result=%v", err)
			}
			if _, err := os.Stat(destination); !os.IsNotExist(err) {
				t.Fatalf("incomplete snapshot published: %v", err)
			}
		})
	}
}
