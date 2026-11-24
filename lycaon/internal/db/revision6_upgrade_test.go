package db

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRevision6UpgradesTheShippedRevision5Store(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(corpusDir(), "1.0.3-rc2", "store.db"))
	testutil.FailErr(t, "read released store", err)
	path := filepath.Join(t.TempDir(), "store.db")
	testutil.FailErr(t, "copy released store", os.WriteFile(path, raw, 0o600))
	testutil.FailErr(t, "upgrade", UpgradeStaged(t.Context(), path))
	testutil.FailErr(t, "reopen", UpgradeStaged(t.Context(), path))
	upgraded, err := sql.Open("sqlite", path)
	testutil.FailErr(t, "open upgraded store", err)
	defer upgraded.Close()
	diff, err := BaselineShapeDiff(t.Context(), upgraded)
	testutil.FailErr(t, "compare with fresh target", err)
	if len(diff) != 0 {
		t.Fatalf("upgraded shape differs: %v", diff)
	}
	var applied int
	testutil.FailErr(t, "read ledger", upgraded.QueryRow(
		`SELECT count(*) FROM schema_migrations WHERE id='home-starts' AND from_revision=5 AND to_revision=6`).Scan(&applied))
	if applied != 1 {
		t.Fatalf("home-starts applied %d times", applied)
	}
	var starts int
	testutil.FailErr(t, "count starts", upgraded.QueryRow(`SELECT count(*) FROM home_starts`).Scan(&starts))
	if starts != 0 {
		t.Fatalf("upgrade fabricated %d Home starts", starts)
	}
}
