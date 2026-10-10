package db

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestSourcePathLookupUsesIndexedNamespaceEdges(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "store.db"))
	testutil.FailErr(t, "open namespace", err)
	t.Cleanup(func() { _ = database.Close() })
	rows, err := database.QueryContext(t.Context(), "EXPLAIN QUERY PLAN "+getSourceBranchHeadByPath, "project", "", "a/b/c", "root")
	testutil.FailErr(t, "explain current path lookup", err)
	defer func() { _ = rows.Close() }()
	indexedRoot, indexedChild, indexedEntry, indexedHead := false, false, false, false
	for rows.Next() {
		var id, parent, unused int
		var detail string
		testutil.FailErr(t, "read lookup plan", rows.Scan(&id, &parent, &unused, &detail))
		for _, table := range []string{"root", "d", "e", "h"} {
			if strings.HasPrefix(detail, "SCAN "+table+" ") || detail == "SCAN "+table {
				t.Fatalf("point lookup scans stored namespace: %s", detail)
			}
		}
		indexedRoot = indexedRoot || strings.Contains(detail, "SEARCH root USING INDEX idx_source_directories_root")
		indexedChild = indexedChild || strings.Contains(detail, "SEARCH d USING INDEX idx_source_directories_name")
		indexedEntry = indexedEntry || strings.Contains(detail, "SEARCH e USING INDEX idx_source_head_entries_live_name")
		indexedHead = indexedHead || strings.Contains(detail, "SEARCH h USING PRIMARY KEY")
	}
	testutil.FailErr(t, "finish lookup plan", rows.Err())
	if !indexedRoot || !indexedChild || !indexedEntry || !indexedHead {
		t.Fatalf("missing indexed point lookup: root=%v child=%v entry=%v head=%v", indexedRoot, indexedChild, indexedEntry, indexedHead)
	}
}

func TestSourceSubtreeLookupUsesIndexedNamespaceEdges(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "store.db"))
	testutil.FailErr(t, "open namespace", err)
	t.Cleanup(func() { _ = database.Close() })
	rows, err := database.QueryContext(t.Context(), "EXPLAIN QUERY PLAN "+listSourceBranchHeadsUnderPath, "tree/nested", "project", "", "root")
	testutil.FailErr(t, "explain subtree lookup", err)
	defer func() { _ = rows.Close() }()
	indexedRoot, indexedPath, indexedDescendants := false, false, false
	indexedEntry, indexedHead := false, false
	var plan []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		testutil.FailErr(t, "read subtree plan", rows.Scan(&id, &parent, &unused, &detail))
		plan = append(plan, detail)
		if strings.HasPrefix(detail, "SCAN e") || strings.HasPrefix(detail, "SCAN h") {
			t.Fatalf("subtree scans unrelated heads: %s", detail)
		}
		indexedRoot = indexedRoot || strings.Contains(detail, "SEARCH root USING INDEX idx_source_directories_root")
		indexedPath = indexedPath || strings.Contains(detail, "SEARCH d USING INDEX idx_source_directories_name")
		indexedDescendants = indexedDescendants || strings.Contains(detail, "SEARCH d USING INDEX idx_source_directories_parent")
		indexedEntry = indexedEntry || strings.Contains(detail, "SEARCH e USING INDEX idx_source_head_entries_directory")
		indexedHead = indexedHead || strings.Contains(detail, "SEARCH h USING INDEX idx_source_head_entries_file")
	}
	testutil.FailErr(t, "finish subtree plan", rows.Err())
	if !indexedRoot || !indexedPath || !indexedDescendants || !indexedEntry || !indexedHead {
		t.Fatalf("missing indexed subtree lookup: root=%v path=%v descendants=%v entry=%v head=%v; plan=%q", indexedRoot, indexedPath, indexedDescendants, indexedEntry, indexedHead, plan)
	}
}
