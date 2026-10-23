package db

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/db/migrations"
	"github.com/lycaon/lycaon/internal/testutil"
)

// seedLegacyManifestStore copies the released revision-4 corpus and adds
// bucketed snapshots, an unreferenced chunk, and abandoned staging. It
// returns each seeded snapshot's entries by root as "path|sha|oid|identity|size|mode|mtime".
func seedLegacyManifestStore(t *testing.T, dir string, rootEntries map[string]int) (string, map[string]map[string][]string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(corpusDir(), "1.0.3-rc1", "store.db"))
	testutil.FailErr(t, "read released corpus", err)
	path := filepath.Join(dir, "store.db")
	testutil.FailErr(t, "copy released corpus", os.WriteFile(path, raw, 0o600))
	source, err := sql.Open("sqlite", path)
	testutil.FailErr(t, "open released store", err)
	defer func() { _ = source.Close() }()
	released, err := inspectSchema(t.Context(), source)
	testutil.FailErr(t, "inspect released store", err)
	if released != migrations.SourceManifestLayout(released).From {
		t.Fatalf("released store is %+v, not the registered revision-4 source", released)
	}
	want := map[string]map[string][]string{}
	exec := func(query string, args ...any) {
		t.Helper()
		_, err := source.Exec(query, args...)
		testutil.FailErr(t, "seed legacy layout", err)
	}
	seedSnapshot := func(id string, roots []string) {
		total := 0
		want[id] = map[string][]string{}
		for ordinal, root := range roots {
			exec(`INSERT INTO source_snapshot_roots (snapshot_id, root_path, ordinal) VALUES (?, ?, ?)`, id, root, ordinal)
			total += rootEntries[root]
		}
		exec(`INSERT INTO source_snapshots (id, roots_key, merkle_sha256, file_count, total_bytes, capture_quality, created_ts)
			VALUES (?, ?, ?, ?, 0, 'exact', '2026-10-20T00:00:00.000000000Z')`, id, "key-"+id, id, total)
		for bucket := range 4 {
			chunk := fmt.Sprintf("%s-bucket-%d", id, bucket)
			exec(`INSERT INTO source_manifest_chunks (id, entry_count, total_bytes) VALUES (?, 1, 0)`, chunk)
			exec(`INSERT INTO source_snapshot_chunks (snapshot_id, bucket, chunk_id) VALUES (?, ?, ?)`, id, bucket, chunk)
		}
		ordinal := 0
		for _, root := range roots {
			for i := range rootEntries[root] {
				ordinal++
				rel := fmt.Sprintf("dir-%d/file-%05d.go", i%7, i)
				sha, oid, identity := "", fmt.Sprintf("%040x", i), "index"
				if i%3 == 0 {
					sha, oid, identity = fmt.Sprintf("%064x", i), "", "hashed"
				}
				exec(`INSERT INTO source_manifest_entries (chunk_id, ordinal, root_path, path, sha256, git_oid, identity, size, mode, modified_ns)
					VALUES (?, ?, ?, ?, ?, ?, ?, ?, 420, ?)`, fmt.Sprintf("%s-bucket-%d", id, i%4), ordinal, root, rel, sha, oid, identity, i, i*1000)
				want[id][root] = append(want[id][root], fmt.Sprintf("%s|%s|%s|%s|%d|420|%d", rel, sha, oid, identity, i, i*1000))
			}
			sort.Strings(want[id][root])
		}
	}
	seedSnapshot("snapshot-one", []string{"/work/a"})
	seedSnapshot("snapshot-two", []string{"/work/a", "/work/b"})
	exec(`INSERT INTO source_manifest_chunks (id, entry_count, total_bytes) VALUES ('orphan', 1, 0)`)
	exec(`INSERT INTO source_blob_objects (sha256, size, stored_size, storage_relpath, git_oid_sha1, git_oid_sha256)
		VALUES (?, 1, 1, 'objects/orphan', '', '')`, orphanBlob)
	exec(`DELETE FROM source_blob_reclaim_queue`)
	exec(`INSERT INTO source_manifest_entries (chunk_id, ordinal, root_path, path, sha256, identity, size, mode)
		VALUES ('orphan', 0, '/gone', 'stale.go', ?, 'hashed', 1, 420)`, orphanBlob)
	exec(`INSERT INTO source_manifest_staging (build_id, bucket, root_path, path, identity, size, mode)
		VALUES ('abandoned-1', 0, '/work/a', 'half.go', 'stat', 1, 420)`)
	return path, want
}

// orphanBlob is retained content only an unreferenced legacy chunk named.
var orphanBlob = strings.Repeat("ab", 32)

func corpusDir() string { return filepath.Join("..", "..", "testdata", "upgrade-corpus") }

func TestSourceManifestLayoutMigrationKeepsRetainedSnapshots(t *testing.T) {
	path, want := seedLegacyManifestStore(t, t.TempDir(), map[string]int{"/work/a": 9000, "/work/b": 40})
	testutil.FailErr(t, "upgrade released store", UpgradeStaged(t.Context(), path))
	upgraded, err := sql.Open("sqlite", path)
	testutil.FailErr(t, "open upgraded store", err)
	defer func() { _ = upgraded.Close() }()
	current, err := CurrentBaseline(t.Context())
	testutil.FailErr(t, "read current baseline", err)
	got, err := inspectSchema(t.Context(), upgraded)
	testutil.FailErr(t, "inspect upgraded store", err)
	if got != current {
		t.Fatalf("upgraded baseline %+v, want %+v", got, current)
	}
	for snapshot, roots := range want {
		for root, entries := range roots {
			rows, err := upgraded.Query(`SELECT e.path, e.sha256, e.git_oid, e.identity, e.size, e.mode, e.modified_ns
				FROM source_snapshot_roots r JOIN source_manifest_layout l ON l.manifest_id = r.manifest_id
				JOIN source_manifest_entries e ON e.chunk_id = l.chunk_id
				WHERE r.snapshot_id = ? AND r.root_path = ? ORDER BY e.path`, snapshot, root)
			testutil.FailErr(t, "read converted entries", err)
			var converted []string
			for rows.Next() {
				var p, sha, oid, identity string
				var size, mode, mtime int64
				testutil.FailErr(t, "scan converted entry", rows.Scan(&p, &sha, &oid, &identity, &size, &mode, &mtime))
				converted = append(converted, fmt.Sprintf("%s|%s|%s|%s|%d|%d|%d", p, sha, oid, identity, size, mode, mtime))
			}
			testutil.FailErr(t, "close converted rows", rows.Close())
			if strings.Join(converted, "\n") != strings.Join(entries, "\n") {
				t.Fatalf("%s %s converted %d entries, want %d retained", snapshot, root, len(converted), len(entries))
			}
		}
	}
	var manifests, layoutChunks, orphan, staging int
	testutil.FailErr(t, "count manifests", upgraded.QueryRow(`SELECT COUNT(*) FROM source_manifests`).Scan(&manifests))
	testutil.FailErr(t, "count layout", upgraded.QueryRow(`SELECT COUNT(*) FROM source_manifest_layout`).Scan(&layoutChunks))
	testutil.FailErr(t, "count orphan", upgraded.QueryRow(`SELECT COUNT(*) FROM source_manifest_entries WHERE path = 'stale.go'`).Scan(&orphan))
	testutil.FailErr(t, "count staging", upgraded.QueryRow(`SELECT COUNT(*) FROM source_manifest_staging`).Scan(&staging))
	// The corpus snapshot, /work/a shared by both seeded snapshots, and /work/b.
	if manifests != 3 {
		t.Fatalf("manifests = %d, want shared roots to share one", manifests)
	}
	if layoutChunks < 3 {
		t.Fatalf("a 9000-entry root converted into %d chunks", layoutChunks)
	}
	if orphan != 0 || staging != 0 {
		t.Fatalf("migration kept unreferenced rows: orphan=%d staging=%d", orphan, staging)
	}
	var queued int
	testutil.FailErr(t, "read blob reclaim queue", upgraded.QueryRow(`SELECT COUNT(*) FROM source_blob_reclaim_queue WHERE sha256 = ?`, orphanBlob).Scan(&queued))
	if queued != 1 {
		t.Fatal("content only dropped entries named was not queued for reclaim")
	}
	var legacy int
	testutil.FailErr(t, "count legacy tables", upgraded.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name LIKE 'legacy_%'`).Scan(&legacy))
	if legacy != 0 {
		t.Fatalf("migration left %d legacy schema objects", legacy)
	}
}

// The conversion's write bound follows what it retains, not the store size.
func TestSourceManifestLayoutBoundsItsWrites(t *testing.T) {
	path, _ := seedLegacyManifestStore(t, t.TempDir(), map[string]int{"/work/a": 900, "/work/b": 40})
	source, err := sql.Open("sqlite", path)
	testutil.FailErr(t, "open seeded store", err)
	defer func() { _ = source.Close() }()
	released, err := inspectSchema(t.Context(), source)
	testutil.FailErr(t, "inspect seeded store", err)
	step := migrations.SourceManifestLayout(released)
	const storeBytes = 40 << 30
	writes, err := step.Writes(t.Context(), source, storeBytes)
	testutil.FailErr(t, "estimate writes", err)
	if writes >= storeBytes/32 || writes < (900+900+40)*1024 {
		t.Fatalf("write bound = %d for a %d-byte store", writes, uint64(storeBytes))
	}
}
