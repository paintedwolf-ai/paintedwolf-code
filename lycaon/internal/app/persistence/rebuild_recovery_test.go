package persistence

import (
	"archive/zip"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/backup"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRebuiltUpgradeRetainsRestorableInstallation(t *testing.T) {
	root := t.TempDir()
	archive, err := zip.OpenReader(filepath.Join("..", "..", "..", "testdata", "upgrade-corpus", "1.0.3-rc1", "backup.zip"))
	testutil.FailErr(t, "open released installation fixture", err)
	testutil.FailErr(t, "extract trusted fixture", os.CopyFS(root, archive))
	testutil.FailErr(t, "close fixture", archive.Close())
	path := filepath.Join(root, "store.db")
	builder := &Runtime{logger: slog.Default()}
	database, err := builder.openUpgradeableStore(t.Context(), path)
	testutil.FailErr(t, "upgrade retained installation", err)
	defer func() { _ = database.Close() }()
	snapshot, record, err := backup.LatestUpgradeRecovery(t.Context(), root)
	testutil.FailErr(t, "find compact recovery", err)
	if record.Source.Revision != 4 || record.Target.Revision != db.SchemaVersion {
		t.Fatalf("recovery route = %v -> %v", record.Source, record.Target)
	}
	before, err := db.OpenReadOnly(t.Context(), filepath.Join(snapshot, "store.db"))
	testutil.FailErr(t, "open pre-upgrade recovery", err)
	var revision int
	testutil.FailErr(t, "read recovery revision", before.QueryRow(`PRAGMA user_version`).Scan(&revision))
	if revision != 4 {
		t.Fatalf("recovery was mutated by upgrade: revision %d", revision)
	}
	var integrity string
	testutil.FailErr(t, "check compact recovery", before.QueryRow(`PRAGMA integrity_check`).Scan(&integrity))
	if integrity != "ok" {
		t.Fatalf("compact recovery integrity: %s", integrity)
	}
	testutil.FailErr(t, "close compact recovery", before.Close())
	if builder.UpgradeReady == nil {
		t.Fatal("upgrade has no readiness completion")
	}
	testutil.FailErr(t, "complete readiness", builder.UpgradeReady())
	entries, err := os.ReadDir(root)
	testutil.FailErr(t, "inspect publication cleanup", err)
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".store-upgrade-") || strings.HasSuffix(entry.Name(), ".upgrade.json") {
			t.Fatalf("completed upgrade retained raw publication work: %s", entry.Name())
		}
	}
	// Restore from the self-contained recovery, with no original live files.
	restored := t.TempDir()
	testutil.FailErr(t, "copy recovery into separate installation", os.CopyFS(filepath.Join(restored, db.UpgradeRecoveryDirName), os.DirFS(filepath.Join(root, db.UpgradeRecoveryDirName))))
	_, err = backup.StageLatestUpgradeRecovery(t.Context(), backup.StageOpts{ConfigDir: restored, SchemaVersion: db.SchemaVersion})
	testutil.FailErr(t, "stage compact recovery", err)
	testutil.FailErr(t, "apply compact recovery", backup.ApplyPending(t.Context(), restored))
	result, err := db.Open(filepath.Join(restored, "store.db"))
	testutil.FailErr(t, "open restored installation", err)
	defer func() { _ = result.Close() }()
	testutil.FailErr(t, "validate restored history payloads", backup.ValidateLiveReferences(t.Context(), result, restored))
	for _, table := range []string{"messages", "editor_documents", "source_snapshots", "source_snapshot_roots", "source_manifest_entries", "worker_jobs"} {
		var want, got int
		testutil.FailErr(t, "count original upgraded rows", database.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM "+table).Scan(&want))
		testutil.FailErr(t, "count restored rows", result.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM "+table).Scan(&got))
		if got != want {
			t.Fatalf("%s restored rows = %d, want %d", table, got, want)
		}
	}
}
