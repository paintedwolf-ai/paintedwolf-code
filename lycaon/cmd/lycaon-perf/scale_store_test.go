package main

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCountOwnedRowsFollowsCascadingKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	store, err := db.Open(path)
	testutil.FailErr(t, "open store", err)
	for _, project := range []string{"owned", "other"} {
		testdbseed.InsertProjectRootWithID(t, store, project, project+"-root", filepath.Join(t.TempDir(), project))
	}
	for _, statement := range []string{
		`INSERT INTO source_files (id, project_id, entry_kind, created_ts) VALUES ('f1', 'owned', 'file', 'now'), ('f2', 'owned', 'file', 'now'), ('f3', 'other', 'file', 'now')`,
		`INSERT INTO source_versions (id, file_id, project_id, root_id, path, state, content_sha256, byte_size, capture_state, capture_quality, created_ts, seq)
VALUES ('v1', 'f1', 'owned', 'owned-root', 'a', 'content', 'aa', 1, 'metadata_only', 'exact', 'now', 1)`,
	} {
		_, err := store.ExecContext(t.Context(), statement)
		testutil.FailErr(t, "seed rows", err)
	}
	testutil.FailErr(t, "close store", store.Close())
	owned, err := countOwnedRows(t.Context(), path, "owned")
	testutil.FailErr(t, "count owned rows", err)
	// The project row, its root, two files and one version.
	if owned != 5 {
		t.Fatalf("owned rows = %d, want 5", owned)
	}
}

func TestReleasedCorpusTakesAnInventoryOfEverySize(t *testing.T) {
	config := filepath.Join(t.TempDir(), "config")
	testutil.FailErr(t, "materialize corpus", materializeCorpus(filepath.Join("..", "..", "testdata", "upgrade-corpus", "1.0.2"), config))
	store := filepath.Join(config, "store.db")
	before, err := readStoreShape(t.Context(), store)
	testutil.FailErr(t, "read shape", err)
	const files = 1_300
	testutil.FailErr(t, "seed inventory", seedReleasedInventory(t.Context(), store, files))
	database, err := openFixtureStore(store)
	testutil.FailErr(t, "open store", err)
	defer func() { _ = database.Close() }()
	var entries, chunks, snapshotFiles int
	testutil.FailErr(t, "count entries", database.QueryRowContext(t.Context(),
		`SELECT count(*) FROM source_manifest_entries WHERE root_path = ?`, releasedRoot).Scan(&entries))
	testutil.FailErr(t, "count chunks", database.QueryRowContext(t.Context(),
		`SELECT count(*), max(file_count) FROM source_snapshot_chunks c JOIN source_snapshots s ON s.id = c.snapshot_id WHERE s.roots_key = ?`, releasedRoot).Scan(&chunks, &snapshotFiles))
	if entries != files || snapshotFiles != files || chunks < 200 || chunks > 256 {
		t.Fatalf("entries=%d snapshot files=%d chunks=%d", entries, snapshotFiles, chunks)
	}
	after, err := readStoreShape(t.Context(), store)
	testutil.FailErr(t, "read shape", err)
	if after.pages <= before.pages {
		t.Fatalf("store pages %d did not grow from %d", after.pages, before.pages)
	}
}
