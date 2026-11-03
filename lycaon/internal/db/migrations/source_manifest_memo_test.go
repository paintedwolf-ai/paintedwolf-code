package migrations

import (
	"fmt"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestMigrationMemoRetentionIsIndependentOfHistoryLength(t *testing.T) {
	for _, limit := range []int64{16, 64, 512} {
		memo := newMigrationMemo[int](limit)
		latest := map[string]int{}
		for sequence := range 10000 {
			key := fmt.Sprintf("key-%d", sequence%17)
			latest[key] = sequence
			memo.store(key, sequence, int64(len(key)+sequence%23+8))
			for _, candidate := range []string{key, "key-0", "key-9"} {
				if value, ok := memo.load(candidate); ok && value != latest[candidate] {
					t.Fatalf("eviction or replacement changed a retained value: %s=%d", candidate, value)
				}
			}
			if memo.used < 0 || memo.used > limit || int64(len(memo.entries)) > limit {
				t.Fatalf("history length widened memo bound: step=%d used=%d entries=%d limit=%d", sequence, memo.used, len(memo.entries), limit)
			}
		}
		memo.store("oversized", 1, limit+1)
		if _, ok := memo.load("oversized"); ok {
			t.Fatal("oversized work was retained between roots")
		}
	}
}

func TestManifestConversionIdentitySurvivesEveryMemoEviction(t *testing.T) {
	database, trace := newLegacyMigrationDatabase(t)
	for _, chunk := range []struct{ id, path, oid string }{
		{"shared", "shared.go", "1111"}, {"a", "delta.go", "2222"}, {"b", "delta.go", "3333"},
	} {
		_, err := database.Exec(`INSERT INTO source_manifest_chunks(id,entry_count,total_bytes) VALUES(?,1,1)`, chunk.id)
		testutil.FailErr(t, "seed chunk", err)
		_, err = database.Exec(`INSERT INTO source_manifest_entries(chunk_id,ordinal,root_path,path,git_oid,identity,size,mode,modified_ns)
VALUES(?,0,'/root',?,?,'index',1,420,1)`, chunk.id, chunk.path, chunk.oid)
		testutil.FailErr(t, "seed chunk entry", err)
	}
	*trace = manifestMigrationTrace{}
	tx, err := database.BeginTx(t.Context(), nil)
	testutil.FailErr(t, "begin bounded conversion", err)
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(t.Context(), string(manifestLayoutSchema))
	testutil.FailErr(t, "create target tables and indexes", err)
	insert, err := prepareLayoutEntryInsert(t.Context(), tx, layoutInsertRows)
	testutil.FailErr(t, "prepare target entries", err)
	defer func() { _ = insert.Close() }()
	read, err := tx.PrepareContext(t.Context(), `SELECT root_path,path,sha256,git_oid,identity,size,mode,modified_ns FROM legacy_source_manifest_entries WHERE chunk_id = ?`)
	testutil.FailErr(t, "prepare legacy chunk read", err)
	defer func() { _ = read.Close() }()
	converter := newLegacyManifestConverter(tx, insert, read)
	converter.chunks.limit, converter.manifests.limit, converter.writtenChunks.limit = 1, 1, 1
	var identities []string
	for _, chunks := range [][]string{{"shared", "a"}, {"shared", "b"}, {"shared", "a"}} {
		id, err := converter.convertRoot(t.Context(), "/root", chunks)
		testutil.FailErr(t, "convert with evicted memos", err)
		identities = append(identities, id)
		if converter.chunks.used > 1 || converter.manifests.used > 1 || converter.writtenChunks.used > 1 {
			t.Fatal("conversion widened a memo's retention allowance")
		}
	}
	if identities[0] == "" || identities[0] != identities[2] || identities[0] == identities[1] {
		t.Fatalf("memo eviction changed content identity: %v", identities)
	}
	var manifests, entries int
	testutil.FailErr(t, "count canonical manifests", tx.QueryRow(`SELECT COUNT(*) FROM source_manifests`).Scan(&manifests))
	testutil.FailErr(t, "count amplified target entries", tx.QueryRow(`SELECT COUNT(*) FROM source_manifest_entries`).Scan(&entries))
	if manifests != 2 || entries != 4 {
		t.Fatalf("eviction duplicated or lost canonical data: manifests=%d entries=%d", manifests, entries)
	}
	if trace.indexesAfterInserts != 0 {
		t.Fatal("conversion bulk-sorted amplified target rows")
	}
}
