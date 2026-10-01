package backup_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/backup"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/editoroutbox"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
)

// seedEditorOutbox writes one committed native transaction and returns its record log path and bytes.
func seedEditorOutbox(t *testing.T, root string) (string, []byte) {
	t.Helper()
	frame := func(record string, offset int) (map[string]any, []byte) {
		raw := []byte(record + "\n")
		var identity struct {
			Kind        string `json:"kind"`
			ClientID    string `json:"clientId"`
			OperationID string `json:"operationId"`
		}
		testutil.FailErr(t, "decode record identity", json.Unmarshal(raw, &identity))
		if identity.Kind == "checkpoint" {
			identity.OperationID = "checkpoint"
		}
		name := sha256.Sum256([]byte(identity.ClientID + "\x00" + identity.Kind + "\x00" + identity.OperationID))
		sum := sha256.Sum256(raw)
		return map[string]any{"name": hex.EncodeToString(name[:]), "kind": identity.Kind, "offset": offset, "length": len(raw), "sha256": hex.EncodeToString(sum[:])}, raw
	}
	checkpoint, checkpointRaw := frame(`{"kind":"checkpoint","documentId":"document","clientId":"window","state":"AAA=","history":{"doc":"","history":{"done":[],"undone":[]}},"synchronized":false}`, 0)
	checkpoint["pending"], checkpoint["synchronized"] = []string{"pending"}, false
	update, updateRaw := frame(`{"kind":"update","documentId":"document","clientId":"window","operationId":"pending","update":"AAA=","acknowledged":false}`, len(checkpointRaw))
	records := slices.Concat(checkpointRaw, updateRaw)
	header, err := json.Marshal(map[string]any{"format": 1, "address": map[string]string{"documentId": "document", "projectId": "project", "rootId": "root", "fileId": "file", "path": "file.txt"}, "updatedAt": 1, "synchronized": false, "log": "records-1.log", "logBytes": len(records), "frames": []map[string]any{checkpoint, update}})
	testutil.FailErr(t, "encode native header", err)
	id := sha256.Sum256([]byte("document"))
	directory := filepath.Join(editoroutbox.Directory(), hex.EncodeToString(id[:]))
	testutil.FailErr(t, "create outbox", os.MkdirAll(filepath.Join(root, directory), 0o700))
	testutil.FailErr(t, "preserve pending update and undo", os.WriteFile(filepath.Join(root, directory, "records-1.log"), records, 0o600))
	testutil.FailErr(t, "publish native transaction", os.WriteFile(filepath.Join(root, directory, "header.json"), header, 0o600))
	return filepath.Join(directory, "records-1.log"), records
}

func TestEditorOutboxSurvivesRelocatedRestoreAndRecoveryPreimage(t *testing.T) {
	source := t.TempDir()
	sourceDB := testdbfixture.OpenPath(t, filepath.Join(source, "store.db"))
	relative, want := seedEditorOutbox(t, source)
	archive, _, err := createArchive(t, t.Context(), backup.CreateOpts{ConfigDir: source, DBPath: filepath.Join(source, "store.db"), SQLDB: sourceDB, SchemaUserVersion: db.SchemaVersion, AppVersion: "test"})
	testutil.FailErr(t, "capture collaborative work", err)
	target := t.TempDir()
	targetDB := testdbfixture.OpenPath(t, filepath.Join(target, "store.db"))
	seedEditorOutbox(t, target)
	result, err := backup.Stage(t.Context(), backup.StageOpts{ConfigDir: target, DBPath: filepath.Join(target, "store.db"), SQLDB: targetDB, ArchivePath: archivePath(t, archive), SchemaVersion: db.SchemaVersion})
	testutil.FailErr(t, "stage relocated installation", err)
	testutil.FailErr(t, "close target store", targetDB.Close())
	testutil.FailErr(t, "apply relocated installation", backup.ApplyPending(target))
	testutil.FailErr(t, "validate restored editor envelope", editoroutbox.Validate(t.Context(), target))
	for _, root := range []string{target, result.RecoveryCopyPath} {
		actual, err := os.ReadFile(filepath.Join(root, relative))
		testutil.FailErr(t, "read retained editor work", err)
		if string(actual) != string(want) {
			t.Fatal("restore lost pending update or window undo")
		}
	}
}

func TestBackupWaitsForNativeOutboxTransactionBeforeSnapshot(t *testing.T) {
	root := t.TempDir()
	database := testdbfixture.OpenPath(t, filepath.Join(root, "store.db"))
	release, err := editoroutbox.Acquire(t.Context(), root)
	testutil.FailErr(t, "hold native transaction", err)
	defer release()
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	_, err = backup.Create(ctx, backup.CreateOpts{ConfigDir: root, SQLDB: database, DBPath: filepath.Join(root, "store.db"), SchemaUserVersion: db.SchemaVersion}, filepath.Join(t.TempDir(), "backup.zip"))
	if err == nil {
		t.Fatal("backup captured through a native write")
	}
}

func TestExplicitFreshStartRetainsOldEditorWorkWithItsRecoveryDatabase(t *testing.T) {
	root := t.TempDir()
	database := testdbfixture.OpenPath(t, filepath.Join(root, "store.db"))
	relative, want := seedEditorOutbox(t, root)
	result, err := backup.StageFreshStart(t.Context(), backup.FreshStartOpts{ConfigDir: root, DBPath: filepath.Join(root, "store.db"), SQLDB: database, AppVersion: "test"})
	testutil.FailErr(t, "stage explicit fresh start", err)
	testutil.FailErr(t, "close old store", database.Close())
	testutil.FailErr(t, "apply explicit fresh start", backup.ApplyPending(root))
	if _, err := os.Stat(filepath.Join(root, relative)); !os.IsNotExist(err) {
		t.Fatalf("old editor identity remained beside a fresh store: %v", err)
	}
	retained, err := os.ReadFile(filepath.Join(result.RecoveryCopyPath, relative))
	testutil.FailErr(t, "read old editor recovery work", err)
	if string(retained) != string(want) {
		t.Fatal("fresh-start recovery lost pending editor work")
	}
}
