package sourcesnapshot

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/backgroundwork"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/sourceblob"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
)

// A snapshot converted by the layout migration names the manifest a capture
// of the same entries builds, so converted and captured generations dedupe.
func TestMigratedManifestMatchesCapturedManifest(t *testing.T) {
	dir := t.TempDir()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "upgrade-corpus", "1.0.3-rc1", "store.db"))
	testutil.FailErr(t, "read released corpus", err)
	path := filepath.Join(dir, "store.db")
	testutil.FailErr(t, "copy released corpus", os.WriteFile(path, raw, 0o600))
	legacy, err := sql.Open("sqlite", path)
	testutil.FailErr(t, "open released store", err)
	const root = "/work/tree"
	seed := func(query string, args ...any) {
		t.Helper()
		_, err := legacy.Exec(query, args...)
		testutil.FailErr(t, "seed legacy snapshot", err)
	}
	const files = 3000
	seed(`INSERT INTO source_snapshots (id, roots_key, merkle_sha256, file_count, total_bytes, capture_quality, created_ts)
		VALUES ('legacy', 'k', 'legacy', ?, 0, 'exact', '2026-10-20T00:00:00.000000000Z')`, files)
	seed(`INSERT INTO source_snapshot_roots (snapshot_id, root_path, ordinal) VALUES ('legacy', ?, 0)`, root)
	seed(`INSERT INTO source_manifest_chunks (id, entry_count, total_bytes) VALUES ('bucket', ?, 0)`, files)
	seed(`INSERT INTO source_snapshot_chunks (snapshot_id, bucket, chunk_id) VALUES ('legacy', 0, 'bucket')`)
	var entries []Entry
	for i := range files {
		entry := Entry{
			Path: fmt.Sprintf("src/%04d/file.go", (i*7919)%files), GitOID: fmt.Sprintf("%040x", i),
			Identity: IdentityIndex, Size: int64(i), Mode: 0o644, ModifiedNS: int64(i),
		}
		if i%5 == 0 {
			entry.GitOID, entry.SHA256, entry.Identity = "", fmt.Sprintf("%064x", i), IdentityHashed
		}
		seed(`INSERT INTO source_manifest_entries (chunk_id, ordinal, root_path, path, sha256, git_oid, identity, size, mode, modified_ns)
			VALUES ('bucket', ?, ?, ?, ?, ?, ?, ?, ?, ?)`, i, root, entry.Path, entry.SHA256, entry.GitOID, string(entry.Identity), entry.Size, int64(entry.Mode), entry.ModifiedNS)
		entries = append(entries, entry)
	}
	testutil.FailErr(t, "close legacy store", legacy.Close())
	testutil.FailErr(t, "upgrade store", db.UpgradeStaged(t.Context(), path))

	database := testdbfixture.OpenPath(t, path)
	broker := backgroundwork.New(map[backgroundwork.Resource]backgroundwork.Limits{backgroundwork.ResourceIO: {Total: 1}})
	store := New(database, sourceblob.New(filepath.Join(dir, "content")), filepath.Join(dir, "observations.db"), broker)
	t.Cleanup(func() { _ = store.Close() })
	var migrated string
	testutil.FailErr(t, "read migrated manifest", store.db.QueryRowContext(t.Context(),
		`SELECT manifest_id FROM source_snapshot_roots WHERE snapshot_id = 'legacy'`).Scan(&migrated))
	sortByPath(entries)
	captured, _ := buildEntries(t, store, entries)
	if captured.id != migrated {
		t.Fatalf("captured manifest %s, migrated manifest %s", captured.id, migrated)
	}
	got, err := store.EntriesUnder(t.Context(), "legacy", root, ".")
	testutil.FailErr(t, "read migrated entries", err)
	if len(got) != files || got[0].RootPath != root {
		t.Fatalf("migrated snapshot lists %d entries", len(got))
	}
}

func sortByPath(entries []Entry) {
	byPath := make(map[string]Entry, len(entries))
	for _, entry := range entries {
		byPath[entry.Path] = entry
	}
	for i, path := range sortedKeys(byPath) {
		entries[i] = byPath[path]
	}
}
