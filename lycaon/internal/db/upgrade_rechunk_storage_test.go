package db

import (
	"database/sql"
	"fmt"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRetainedStorageCapsAmplifiedRechunkedRows(t *testing.T) {
	path, _ := seedLegacyManifestStore(t, t.TempDir(), map[string]int{"/work/a": 0, "/work/b": 0})
	writer, err := sql.Open("sqlite", path)
	testutil.FailErr(t, "open shared legacy fixture", err)
	tx, err := writer.BeginTx(t.Context(), nil)
	testutil.FailErr(t, "begin shared fixture", err)
	exec := func(query string, args ...any) {
		t.Helper()
		_, err := tx.ExecContext(t.Context(), query, args...)
		testutil.FailErr(t, "seed shared fixture", err)
	}
	exec("INSERT INTO source_manifest_chunks(id,entry_count,total_bytes) VALUES('shared',64,64)")
	for i := range 64 {
		exec(`INSERT INTO source_manifest_entries(chunk_id,ordinal,root_path,path,identity,size,mode) VALUES('shared',?,'/work/a',?,'stat',1,420)`, i, fmt.Sprintf("middle-%02d", i))
	}
	for i := range 5 {
		id := fmt.Sprintf("amplified-%d", i)
		chunk := fmt.Sprintf("neighbor-%d", i)
		exec(`INSERT INTO source_snapshots(id,roots_key,merkle_sha256,file_count,total_bytes,capture_quality,created_ts) VALUES(?,?,?,65,65,'exact','2026-10-20T00:00:00.000000000Z')`, id, id, id)
		exec("INSERT INTO source_snapshot_roots(snapshot_id,root_path,ordinal) VALUES(?,'/work/a',0)", id)
		exec("INSERT INTO source_manifest_chunks(id,entry_count,total_bytes) VALUES(?,1,1)", chunk)
		exec(`INSERT INTO source_manifest_entries(chunk_id,ordinal,root_path,path,identity,size,mode) VALUES(?,0,'/work/a',?,'stat',1,420)`, chunk, chunk)
		exec("INSERT INTO source_snapshot_chunks(snapshot_id,bucket,chunk_id) VALUES(?,0,'shared'),(?,1,?)", id, id, chunk)
	}
	testutil.FailErr(t, "commit shared fixture", tx.Commit())
	var before int
	testutil.FailErr(t, "count unique legacy rows", writer.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM source_manifest_entries").Scan(&before))
	testutil.FailErr(t, "close shared fixture", writer.Close())
	testutil.FailErr(t, "migrate amplified records", UpgradeStaged(t.Context(), path))
	reader, err := openReader(t.Context(), path)
	testutil.FailErr(t, "open amplified target", err)
	defer reader.Close()
	var after, files, amplified int
	testutil.FailErr(t, "count rechunked rows", reader.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM source_manifest_entries").Scan(&after))
	testutil.FailErr(t, "count preserved snapshot files", reader.QueryRowContext(t.Context(), "SELECT SUM(file_count) FROM source_snapshots WHERE id LIKE 'amplified-%'").Scan(&files))
	testutil.FailErr(t, "count amplified manifest rows", reader.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM source_snapshot_roots r
 JOIN source_manifest_layout l ON l.manifest_id=r.manifest_id
 JOIN source_manifest_entries e ON e.chunk_id=l.chunk_id WHERE r.snapshot_id LIKE 'amplified-%'`).Scan(&amplified))
	if amplified != 325 || files != 325 || after <= before {
		t.Fatalf("amplification lost files: original=%d target=%d amplified=%d snapshots=%d", before, after, amplified, files)
	}
}
