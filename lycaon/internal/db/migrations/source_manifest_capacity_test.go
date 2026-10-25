package migrations

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	_ "modernc.org/sqlite"
)

func TestManifestWriteEstimateCountsRetainedTextWithoutGarbage(t *testing.T) {
	store, err := sql.Open("sqlite", ":memory:")
	testutil.FailErr(t, "open fixture", err)
	defer store.Close()
	_, err = store.Exec(`CREATE TABLE source_snapshot_chunks(snapshot_id TEXT, chunk_id TEXT);
 CREATE TABLE source_snapshot_roots(snapshot_id TEXT, root_path TEXT);
 CREATE TABLE source_manifest_entries(chunk_id TEXT, root_path TEXT, path TEXT, sha256 TEXT, git_oid TEXT, identity TEXT);
 INSERT INTO source_snapshot_chunks VALUES('s', 'kept');
 INSERT INTO source_snapshot_roots VALUES('s', '/root');
 INSERT INTO source_manifest_entries VALUES('kept', '/root', 'a', '', '', 'index');`)
	testutil.FailErr(t, "seed fixture", err)
	before, err := manifestLayoutWrites(t.Context(), store, 1<<30)
	testutil.FailErr(t, "estimate short path", err)
	longPath := strings.Repeat("界", 4096)
	_, err = store.Exec(`UPDATE source_manifest_entries SET path = ?`, longPath)
	testutil.FailErr(t, "set long path", err)
	after, err := manifestLayoutWrites(t.Context(), store, 1<<30)
	testutil.FailErr(t, "estimate long path", err)
	if after-before != 4*uint64(len(longPath)-1) {
		t.Fatalf("text growth budget=%d", after-before)
	}
	_, err = store.Exec(`INSERT INTO source_manifest_entries VALUES('garbage', '/root', ?, '', '', 'index')`, longPath)
	testutil.FailErr(t, "add unretained garbage", err)
	garbage, err := manifestLayoutWrites(t.Context(), store, 1<<30)
	testutil.FailErr(t, "estimate with garbage", err)
	if garbage != after {
		t.Fatalf("unretained entry changed write budget: %d != %d", garbage, after)
	}
}
