package backup_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/backup"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRecoveryRestoreInstallsConfiguredDatabaseAndRetainsItsJournals(t *testing.T) {
	source := testdbfixture.Open(t, "store.db")
	sourceRoot, err := db.Directory(t.Context(), source)
	testutil.FailErr(t, "resolve source directory", err)
	_, err = source.ExecContext(t.Context(), `INSERT INTO store_meta(key,value) VALUES('restore-probe','archive')`)
	testutil.FailErr(t, "seed archived value", err)
	archive := filepath.Join(t.TempDir(), "backup.zip")
	_, err = backup.Create(t.Context(), backup.CreateOpts{ConfigDir: sourceRoot, DBPath: filepath.Join(sourceRoot, "store.db"), SQLDB: source, SchemaUserVersion: db.SchemaVersion}, archive)
	testutil.FailErr(t, "capture source", err)
	targetRoot := t.TempDir()
	targetPath := filepath.Join(targetRoot, "custom.sqlite")
	target := testdbfixture.OpenPath(t, targetPath)
	testutil.FailErr(t, "close recovery-mode target", target.Close())
	before, err := os.ReadFile(targetPath)
	testutil.FailErr(t, "read displaced custom store", err)
	testutil.FailErr(t, "seed unrelated canonical filename", os.WriteFile(filepath.Join(targetRoot, "store.db"), []byte("unrelated file"), 0o600))
	testutil.FailErr(t, "seed custom hot journal", os.WriteFile(targetPath+"-wal", []byte("journal bytes"), 0o600))
	staged, err := backup.Stage(t.Context(), backup.StageOpts{ConfigDir: targetRoot, DBPath: targetPath, ArchivePath: archive, SchemaVersion: db.SchemaVersion})
	testutil.FailErr(t, "stage custom recovery target", err)
	preimage, err := os.ReadFile(filepath.Join(staged.RecoveryCopyPath, "store.db"))
	testutil.FailErr(t, "read canonical recovery preimage", err)
	if !bytes.Equal(preimage, before) {
		t.Fatal("recovery copy used unrelated store.db instead of custom database")
	}
	journal, err := os.ReadFile(filepath.Join(staged.RecoveryCopyPath, "store.db-wal"))
	testutil.FailErr(t, "read custom journal preimage", err)
	if string(journal) != "journal bytes" {
		t.Fatalf("preimage journal=%q", journal)
	}
	testutil.FailErr(t, "apply custom store", backup.ApplyPending(t.Context(), targetRoot))
	testutil.FailErr(t, "repeat custom apply", backup.ApplyPending(t.Context(), targetRoot))
	// Opening the restored database can create fresh journals.
	if _, err := os.Stat(targetPath + "-wal"); !os.IsNotExist(err) {
		t.Fatalf("stale custom journal survived: %v", err)
	}
	restored, err := db.OpenReadOnly(t.Context(), targetPath)
	testutil.FailErr(t, "open restored custom store", err)
	defer func() { _ = restored.Close() }()
	var value string
	testutil.FailErr(t, "read restored value", restored.QueryRowContext(t.Context(), `SELECT value FROM store_meta WHERE key='restore-probe'`).Scan(&value))
	if value != "archive" {
		t.Fatalf("restored value=%q", value)
	}
	unrelated, err := os.ReadFile(filepath.Join(targetRoot, "store.db"))
	testutil.FailErr(t, "read unrelated filename", err)
	if string(unrelated) != "unrelated file" {
		t.Fatal("restore replaced unrelated canonical filename")
	}
}

func TestFreshStartReplacesConfiguredCustomStore(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "custom.db")
	database := testdbfixture.OpenPath(t, path)
	_, err := database.ExecContext(t.Context(), `INSERT INTO store_meta(key,value) VALUES('fresh-probe','remove')`)
	testutil.FailErr(t, "seed custom store", err)
	_, err = backup.StageFreshStart(t.Context(), backup.FreshStartOpts{ConfigDir: root, DBPath: path, SQLDB: database})
	testutil.FailErr(t, "stage custom fresh start", err)
	testutil.FailErr(t, "close custom store", database.Close())
	testutil.FailErr(t, "apply custom fresh start", backup.ApplyPending(t.Context(), root))
	fresh := testdbfixture.OpenPath(t, path)
	var count int
	testutil.FailErr(t, "query fresh custom store", fresh.QueryRowContext(t.Context(), `SELECT count(*) FROM store_meta WHERE key='fresh-probe'`).Scan(&count))
	if count != 0 {
		t.Fatal("fresh start left the old custom database in place")
	}
	if _, err := os.Stat(filepath.Join(root, "store.db")); !os.IsNotExist(err) {
		t.Fatalf("fresh start wrote wrong database filename: %v", err)
	}
}
