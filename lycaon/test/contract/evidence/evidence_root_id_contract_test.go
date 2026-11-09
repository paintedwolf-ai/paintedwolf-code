package contract

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// TestEvidenceRecordsDeclareRootIDLast locks the durable schema shape: every
// evidence record carries the owning root id, declared as the last column so
// the shipped-store ALTER backfill produces the same column order as a fresh
// install.
func TestEvidenceRecordsDeclareRootIDLast(t *testing.T) {
	t.Parallel()
	store := testdbfixture.Open(t, "store.db")
	rows, err := store.QueryContext(t.Context(), `SELECT name, type, "notnull", dflt_value FROM pragma_table_info('evidence_records') ORDER BY cid`)
	contractcheck.FailErr(t, "read evidence_records columns", err)
	defer func() { _ = rows.Close() }()
	type column struct {
		name    string
		typ     string
		notNull bool
		defalt  any
	}
	var columns []column
	for rows.Next() {
		var col column
		contractcheck.FailErr(t, "scan evidence_records column", rows.Scan(&col.name, &col.typ, &col.notNull, &col.defalt))
		columns = append(columns, col)
	}
	contractcheck.FailErr(t, "iterate evidence_records columns", rows.Err())
	if len(columns) == 0 {
		t.Fatal("evidence_records has no columns")
	}
	last := columns[len(columns)-1]
	if last.name != "root_id" || last.typ != "TEXT" || !last.notNull {
		t.Fatalf("last evidence_records column = %+v, want root_id TEXT NOT NULL", last)
	}
	defalt, ok := last.defalt.(string)
	if !ok || defalt != "''" {
		t.Fatalf("root_id default = %v, want ''", last.defalt)
	}
	for _, col := range columns[:len(columns)-1] {
		if col.name == "root_id" {
			t.Fatal("root_id must be declared exactly once, as the last column")
		}
	}
}

// evidenceRootContractRoots builds the two-root fixture used by the citation
// parity checks below.
func evidenceRootContractRoots(t *testing.T) ([]projectroot.RootRef, string, string) {
	t.Helper()
	primary := t.TempDir()
	secondary := t.TempDir()
	return []projectroot.RootRef{
		{ID: "root-p", Label: "main", Path: primary, IsPrimary: true},
		{ID: "root-s", Label: "feature", Path: secondary, IsPrimary: false},
	}, primary, secondary
}

// TestEvidenceCitationsResolveAtEverySessionFolder records the same relative
// file under two roots and verifies that every citation spelling — bare,
// @label, and absolute — resolves to the owning record's ledger key at either
// session folder, and that ResolveCitationFile displays it in the session
// folder's canonical spelling.
func TestEvidenceCitationsResolveAtEverySessionFolder(t *testing.T) {
	t.Parallel()
	refs, primary, secondary := evidenceRootContractRoots(t)
	for _, dir := range []string{filepath.Join(primary, "docs"), filepath.Join(secondary, "docs")} {
		testutil.FailErr(t, "create docs directory", os.MkdirAll(dir, 0o755))
	}
	for _, file := range []string{
		filepath.Join(primary, "docs", "guide.md"),
		filepath.Join(secondary, "docs", "guide.md"),
	} {
		testutil.FailErr(t, "write guide", os.WriteFile(file, []byte("# guide\n"), 0o644))
	}

	records := []evidence.Record{
		{Handle: "read#1", Kind: "file_read", Path: "docs/guide.md", RootID: "root-p"},
		{Handle: "read#2", Kind: "file_read", Path: "docs/guide.md", RootID: "root-s"},
	}

	assertCitation := func(active, token, wantHandle, wantDisplay, wantAbs string) {
		t.Helper()
		ctx := evidence.CitationRoots{Roots: refs, ActiveRootID: active}
		stamped := append([]evidence.Record(nil), records...)
		for i := range stamped {
			evidence.StampDisplayKeys(ctx, &stamped[i])
		}
		ledger := evidence.AssembleLedger(stamped)
		key := evidence.LedgerPathKey(ctx, token, ledger.ByPath)
		handles := ledger.ByPath[key]
		if len(handles) != 1 || handles[0] != wantHandle {
			t.Fatalf("active %s: citation %q keyed %q → %v, want [%s]", active, token, key, handles, wantHandle)
		}
		abs, display, ok := evidence.ResolveCitationFile(ctx, token)
		if !ok || abs != wantAbs || display != wantDisplay {
			t.Fatalf("active %s: citation %q resolved to (%q, %q, %v), want (%q, %q, true)",
				active, token, abs, display, ok, wantAbs, wantDisplay)
		}
	}

	// At the primary's session folder the primary's record stays bare and the
	// secondary's is @label-qualified.
	assertCitation("root-p", "docs/guide.md", "read#1", "docs/guide.md", filepath.Join(primary, "docs", "guide.md"))
	assertCitation("root-p", "@feature/docs/guide.md", "read#2", "@feature/docs/guide.md", filepath.Join(secondary, "docs", "guide.md"))
	assertCitation("root-p", filepath.Join(secondary, "docs", "guide.md"), "read#2", "@feature/docs/guide.md", filepath.Join(secondary, "docs", "guide.md"))

	// At the secondary's session folder the spellings swap.
	assertCitation("root-s", "docs/guide.md", "read#2", "docs/guide.md", filepath.Join(secondary, "docs", "guide.md"))
	assertCitation("root-s", "@main/docs/guide.md", "read#1", "@main/docs/guide.md", filepath.Join(primary, "docs", "guide.md"))
	assertCitation("root-s", filepath.Join(primary, "docs", "guide.md"), "read#1", "@main/docs/guide.md", filepath.Join(primary, "docs", "guide.md"))
}

// TestEvidenceLegacyRowsKeepRootScanFallback locks the compatibility rule:
// records stored before root_id (empty root id, legacy normalized key) are
// still reachable from bare and absolute citations through the multi-root
// scan, at any session folder.
func TestEvidenceLegacyRowsKeepRootScanFallback(t *testing.T) {
	t.Parallel()
	refs, primary, _ := evidenceRootContractRoots(t)
	testutil.FailErr(t, "write legacy file", os.WriteFile(filepath.Join(primary, "legacy.md"), []byte("x\n"), 0o644))
	legacy := evidence.Record{Handle: "read#9", Kind: "file_read", Path: "legacy.md"}
	for _, active := range []string{"root-p", "root-s"} {
		ctx := evidence.CitationRoots{Roots: refs, ActiveRootID: active}
		ledger := evidence.AssembleLedger([]evidence.Record{legacy})
		for _, token := range []string{"legacy.md", filepath.Join(primary, "legacy.md")} {
			key := evidence.LedgerPathKey(ctx, token, ledger.ByPath)
			handles := ledger.ByPath[key]
			if len(handles) != 1 || handles[0] != "read#9" {
				t.Fatalf("active %s: legacy citation %q keyed %q → %v", active, token, key, handles)
			}
		}
	}
}
