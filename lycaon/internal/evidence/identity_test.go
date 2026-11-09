package evidence_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
)

// twoRoots builds a primary root and a labeled secondary root over real
// directories, returning the refs and both host paths.
func twoRoots(t *testing.T) ([]projectroot.RootRef, string, string) {
	t.Helper()
	primary := t.TempDir()
	secondary := t.TempDir()
	return []projectroot.RootRef{
		{ID: "root-p", Label: "main", Path: primary, IsPrimary: true},
		{ID: "root-s", Label: "feature", Path: secondary, IsPrimary: false},
	}, primary, secondary
}

func TestResolvePathIdentity_sessionFolderRule(t *testing.T) {
	refs, primary, secondary := twoRoots(t)
	rel := filepath.Join("docs", "guide.md")
	if err := os.MkdirAll(filepath.Join(secondary, "docs"), 0o755); err != nil {
		testutil.FailErr(t, "create directory", err)
	}
	if err := os.WriteFile(filepath.Join(secondary, rel), []byte("# guide\n"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}

	// The same file cited as @label, absolute, and — under its own active
	// root — bare, all resolve to one durable (root, scope-relative) identity.
	labeled := evidence.ResolvePathIdentity(evidence.CitationRoots{Roots: refs, ActiveRootID: "root-p"}, "@feature/docs/guide.md")
	absolute := evidence.ResolvePathIdentity(evidence.CitationRoots{Roots: refs, ActiveRootID: "root-p"}, filepath.Join(secondary, rel))
	bare := evidence.ResolvePathIdentity(evidence.CitationRoots{Roots: refs, ActiveRootID: "root-s"}, "docs/guide.md")
	for name, id := range map[string]evidence.PathIdentity{"labeled": labeled, "absolute": absolute, "bare": bare} {
		if id.RootID != "root-s" || id.ScopeRel != "docs/guide.md" {
			t.Fatalf("%s identity = %+v, want {root-s docs/guide.md}", name, id)
		}
	}

	// A bare token under the primary's folder pairs with the primary root.
	primaryBare := evidence.ResolvePathIdentity(evidence.CitationRoots{Roots: refs, ActiveRootID: "root-p"}, "README.md")
	if primaryBare.RootID != "root-p" || primaryBare.ScopeRel != "README.md" {
		t.Fatalf("primary bare identity = %+v", primaryBare)
	}

	// Tokens outside every attached root stay legacy with the normalized
	// spelling and an empty root ID.
	outside := evidence.ResolvePathIdentity(evidence.CitationRoots{Roots: refs, ActiveRootID: "root-p"}, "/elsewhere/notes.txt")
	if outside.RootID != "" || outside.ScopeRel != "/elsewhere/notes.txt" {
		t.Fatalf("outside identity = %+v, want legacy /elsewhere/notes.txt", outside)
	}

	// Unknown labels stay legacy too.
	unknown := evidence.ResolvePathIdentity(evidence.CitationRoots{Roots: refs, ActiveRootID: "root-p"}, "@nosuch/x.go")
	if unknown.RootID != "" || unknown.ScopeRel != "@nosuch/x.go" {
		t.Fatalf("unknown label identity = %+v", unknown)
	}

	// Without roots the legacy project-dir rule still applies.
	legacy := evidence.ResolvePathIdentity(evidence.CitationRoots{ProjectDir: primary}, "README.md")
	if legacy.RootID != "" || legacy.ScopeRel != "README.md" {
		t.Fatalf("legacy identity = %+v", legacy)
	}
}

// TestCitationParityAcrossSessionFolders records the same relative file under
// two different roots and verifies every citation spelling resolves to the
// owning record at either session folder.
func TestCitationParityAcrossSessionFolders(t *testing.T) {
	refs, primary, secondary := twoRoots(t)
	underSecondary := evidence.Record{Handle: "read#1", Kind: "file_read", Path: "README.md", RootID: "root-s"}
	underPrimary := evidence.Record{Handle: "read#2", Kind: "file_read", Path: "README.md", RootID: "root-p"}

	assertResolves := func(active string, token, wantHandle string) {
		t.Helper()
		ctx := evidence.CitationRoots{Roots: refs, ActiveRootID: active}
		stamped := []evidence.Record{underSecondary, underPrimary}
		for i := range stamped {
			evidence.StampDisplayKeys(ctx, &stamped[i])
		}
		ledger := evidence.AssembleLedger(stamped)
		key := evidence.LedgerPathKey(ctx, token, ledger.ByPath)
		handles := ledger.ByPath[key]
		if len(handles) != 1 || handles[0] != wantHandle {
			t.Fatalf("active %s: citation %q keyed %q → %v, want [%s]", active, token, key, handles, wantHandle)
		}
	}

	// At the primary's session folder the secondary's record is
	// @label-qualified and the primary's stays bare.
	assertResolves("root-p", "@feature/README.md", "read#1")
	assertResolves("root-p", filepath.Join(secondary, "README.md"), "read#1")
	assertResolves("root-p", "README.md", "read#2")
	assertResolves("root-p", filepath.Join(primary, "README.md"), "read#2")

	// At the secondary's session folder the spellings swap.
	assertResolves("root-s", "README.md", "read#1")
	assertResolves("root-s", filepath.Join(secondary, "README.md"), "read#1")
	assertResolves("root-s", "@main/README.md", "read#2")
	assertResolves("root-s", filepath.Join(primary, "README.md"), "read#2")
}

// TestLegacyRowStillResolvesViaRootScan keeps the pre-root fallback: a record
// stored with an empty root ID and a legacy normalized key is still found by
// a bare citation through the multi-root scan.
func TestLegacyRowStillResolvesViaRootScan(t *testing.T) {
	refs, primary, _ := twoRoots(t)
	if err := os.WriteFile(filepath.Join(primary, "legacy.md"), []byte("x\n"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	legacy := evidence.Record{Handle: "read#9", Kind: "file_read", Path: "legacy.md"}
	ledger := evidence.AssembleLedger([]evidence.Record{legacy})
	ctx := evidence.CitationRoots{Roots: refs, ActiveRootID: "root-p"}
	key := evidence.LedgerPathKey(ctx, "legacy.md", ledger.ByPath)
	if key != "legacy.md" {
		t.Fatalf("legacy key = %q", key)
	}
	if handles := ledger.ByPath[key]; len(handles) != 1 || handles[0] != "read#9" {
		t.Fatalf("legacy handles = %v", handles)
	}
	abs := evidence.LedgerPathKey(ctx, filepath.Join(primary, "legacy.md"), ledger.ByPath)
	if abs != "legacy.md" {
		t.Fatalf("legacy absolute citation keyed %q", abs)
	}
}
