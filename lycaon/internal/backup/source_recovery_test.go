package backup_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/backup"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/sourceblob"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestBackupRequiresTrashRecoveryObjects(t *testing.T) {
	config := t.TempDir()
	dbPath := filepath.Join(config, "store.db")
	database := testdbfixture.OpenPath(t, dbPath)
	testdbseed.InsertProjectRoot(t, database, testdbseed.DefaultProjectID, t.TempDir())
	objectRoot := filepath.Join(config, "source-content")
	ledger := sourceledger.New(database, objectRoot)
	source := filepath.Join(t.TempDir(), "binary")
	testutil.FailErr(t, "seed", os.WriteFile(source, []byte{0, 255, 1}, 0o600))
	scope, err := os.OpenRoot(filepath.Dir(source))
	testutil.FailErr(t, "open source root", err)
	defer func() { _ = scope.Close() }()
	writer, err := ledger.Retention.BeginRecovery(t.Context(), testdbseed.DefaultProjectID, "recovery")
	testutil.FailErr(t, "begin recovery", err)
	defer writer.Close()
	saved, err := writer.Append(t.Context(), sourceledger.RecoveryEntry{Path: ".", Mode: 0o600}, scope, filepath.Base(source), nil)
	testutil.FailErr(t, "retain", err)
	testutil.FailErr(t, "flush recovery", writer.Flush(t.Context()))
	writer.Close()
	opts := backup.CreateOpts{ConfigDir: config, DBPath: dbPath, SQLDB: database, SchemaUserVersion: db.SchemaVersion}
	_, _, err = createArchive(t, t.Context(), opts)
	testutil.FailErr(t, "backup recovery", err)
	rel, err := sourceblob.RelPath(saved.SHA)
	testutil.FailErr(t, "object path", err)
	testutil.FailErr(t, "remove retained object", os.Remove(filepath.Join(objectRoot, rel)))
	_, err = backup.Create(t.Context(), opts, filepath.Join(t.TempDir(), "incomplete.zip"))
	if !errors.Is(err, backup.ErrInvalid) {
		t.Fatalf("backup missing recovery = %v", err)
	}
}
