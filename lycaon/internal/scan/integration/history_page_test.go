package integration

import (
	"fmt"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestScanHistoryPagesSortGloballyAndResolveOldPrefixes(t *testing.T) {
	database := testdbfixture.Open(t, "history.db")
	store := scan.NewSQLStore(database)
	root, err := scan.CanonicalPath(t.TempDir())
	testutil.FailErr(t, "canonicalize scan root", err)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 130; i++ {
		id := fmt.Sprintf("%08x-0000-4000-8000-000000000000", i)
		testutil.FailErr(t, "insert scan", store.Insert(t.Context(), api.CodeScan{ID: id, CanonicalPath: root, ScannerID: fmt.Sprintf("engine-%d", i%3), Status: api.CodeScanStatusComplete, CreatedAt: base.Add(time.Duration(i) * time.Second), Categories: []api.ScanCategory{api.ScanCategorySecurity}}, nil, ""))
	}
	for _, key := range []string{"date", "engine", "status"} {
		for _, order := range []string{"asc", "desc"} {
			t.Run(key+"-"+order, func(t *testing.T) {
				query := scan.PageQuery{Limit: 7, Sort: key, Order: order}
				seen := make(map[string]bool)
				var previous string
				for {
					page, err := store.ListPageByCanonicalPaths(t.Context(), []string{root}, query)
					testutil.FailErr(t, "read page", err)
					if len(page.Scans) > 7 {
						t.Fatalf("page has %d scans", len(page.Scans))
					}
					for _, row := range page.Scans {
						value := row.CreatedAt.Format(time.RFC3339Nano)
						if key == "engine" {
							value = row.ScannerID + value
						}
						if key == "status" {
							value = string(row.Status) + value
						}
						value += row.ID
						if previous != "" && ((order == "asc" && previous >= value) || (order == "desc" && previous <= value)) {
							t.Fatalf("global order %q followed %q", value, previous)
						}
						if seen[row.ID] {
							t.Fatalf("duplicate scan %s", row.ID)
						}
						seen[row.ID] = true
						previous = value
						if len(row.Findings) != 0 || len(row.Result) != 0 {
							t.Fatal("history loaded evidence")
						}
					}
					if page.NextCursor == "" {
						break
					}
					query.Cursor = page.NextCursor
				}
				if len(seen) != 130 {
					t.Fatalf("visited %d scans", len(seen))
				}
			})
		}
	}
	coord := newTestCoordinator(t, store, nil)
	id, err := scan.ResolveProjectScanID(t.Context(), coord, root, "00000000")
	testutil.FailErr(t, "resolve old prefix", err)
	if id != "00000000-0000-4000-8000-000000000000" {
		t.Fatalf("old prefix = %q", id)
	}
	id, err = scan.ResolveProjectScanID(t.Context(), coord, t.TempDir(), id)
	testutil.FailErr(t, "resolve another project", err)
	if id != "" {
		t.Fatalf("cross-project scan = %q", id)
	}
}
