package migrations

import (
	"fmt"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestManifestConversionStreamsARootAcrossChunksInPathOrder(t *testing.T) {
	database, _ := newLegacyMigrationDatabase(t)
	const chunks, perChunk = 3, 2*legacyPageEntries + 37
	var want []string
	for chunk := range chunks {
		id := fmt.Sprintf("chunk-%d", chunk)
		_, err := database.Exec(`INSERT INTO source_manifest_chunks(id,entry_count,total_bytes) VALUES(?,?,?)`, id, perChunk, perChunk)
		testutil.FailErr(t, "seed chunk", err)
		for i := range perChunk {
			// Chunks interleave: chunk c holds paths c, c+3, c+6, ...
			path := fmt.Sprintf("dir/%06d.go", i*chunks+chunk)
			_, err := database.Exec(`INSERT INTO source_manifest_entries(chunk_id,ordinal,root_path,path,git_oid,identity,size,mode,modified_ns)
VALUES(?,?,'/root',?,?,'index',1,420,1)`, id, i, path, fmt.Sprintf("%040d", i))
			testutil.FailErr(t, "seed entry", err)
			want = append(want, path)
		}
	}
	slices.Sort(want)
	tx, err := database.BeginTx(t.Context(), nil)
	testutil.FailErr(t, "begin conversion", err)
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(t.Context(), string(manifestLayoutSchema))
	testutil.FailErr(t, "create target tables", err)
	insert, err := prepareLayoutEntryInsert(t.Context(), tx, layoutInsertRows)
	testutil.FailErr(t, "prepare target entries", err)
	defer func() { _ = insert.Close() }()
	converter, err := newLegacyManifestConverter(t.Context(), tx, insert)
	testutil.FailErr(t, "prepare converter", err)
	defer func() { _ = converter.close() }()
	manifest, err := converter.convertRoot(t.Context(), "/root", []string{"chunk-0", "chunk-1", "chunk-2"})
	testutil.FailErr(t, "convert root", err)
	rows, err := tx.QueryContext(t.Context(), `SELECT e.path FROM source_manifest_layout l
JOIN source_manifest_entries e ON e.chunk_id = l.chunk_id
WHERE l.manifest_id = ? ORDER BY l.first_path, e.path`, manifest)
	testutil.FailErr(t, "read converted manifest", err)
	var got []string
	for rows.Next() {
		var path string
		testutil.FailErr(t, "scan path", rows.Scan(&path))
		got = append(got, path)
	}
	testutil.FailErr(t, "close rows", rows.Close())
	if !slices.Equal(got, want) {
		t.Fatalf("converted %d paths, want the %d legacy paths in order", len(got), len(want))
	}
}
