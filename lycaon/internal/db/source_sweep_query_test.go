package db

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestSourceSweepCandidatesUseIndexedPages(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "store.db"))
	testutil.FailErr(t, "open store", err)
	t.Cleanup(func() { _ = database.Close() })
	for _, tc := range []struct {
		name, query string
		args        []any
	}{
		{"snapshots", listSourceSnapshotSweepCandidates, []any{"2099-01-01", "2000-01-01", "id", 128}},
		{"manifests", listSourceManifestSweepCandidates, []any{"id", 64}},
		{"chunks", listSourceChunkSweepCandidates, []any{"id", 128}},
		{"staging", listSourceStagingSweepCandidates, []any{"build", "path", 2048}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows, err := database.QueryContext(t.Context(), "EXPLAIN QUERY PLAN "+tc.query, tc.args...)
			testutil.FailErr(t, "explain candidates", err)
			defer func() { _ = rows.Close() }()
			indexed := false
			for rows.Next() {
				var id, parent, unused int
				var detail string
				testutil.FailErr(t, "read plan", rows.Scan(&id, &parent, &unused, &detail))
				if strings.HasPrefix(detail, "SCAN ") || strings.Contains(detail, "TEMP B-TREE") {
					t.Fatalf("unbounded candidate work: %s", detail)
				}
				indexed = indexed || strings.HasPrefix(detail, "SEARCH ")
			}
			testutil.FailErr(t, "finish plan", rows.Err())
			if !indexed {
				t.Fatal("candidate page has no indexed seek")
			}
		})
	}
}
