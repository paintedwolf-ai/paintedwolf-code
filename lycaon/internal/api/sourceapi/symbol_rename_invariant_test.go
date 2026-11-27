package sourceapi

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/search"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestSymbolExecutorDoesNotServeStaleDeclarationAfterExternalOverwrite(t *testing.T) {
	e, p, leg := symbolProgressFixture(t, 1)
	root := p.Roots[0]
	renPath := filepath.Join(root.Path, "f000.go")
	v1 := []byte("package p\n\nfunc OldSidecarSym() {}\n")
	v2 := []byte("package p\n\nfunc NewSidecarSym() {}\n")
	testutil.FailErr(t, "write v1", os.WriteFile(renPath, v1, 0o600))

	oldLeg := *leg
	oldLeg.Name = "OldSidecarSym"
	oldLeg.Query = search.TextExpr{Text: "OldSidecarSym"}

	first, err := e.Run(t.Context(), search.PlanLeg{Symbol: &oldLeg})
	testutil.FailErr(t, "initial old symbol search", err)
	if len(first.Hits) != 1 || first.Hits[0].Title != "OldSidecarSym" || len(first.Issues) != 0 {
		t.Fatalf("first = %+v, want 1 OldSidecarSym hit with no issues", first)
	}

	// Overwrite with identical byte length and do NOT call repochange.Advance,
	// reproducing the sidecar window before an async OS watcher event arrives.
	testutil.FailErr(t, "write v2", os.WriteFile(renPath, v2, 0o600))

	afterOld, err := e.Run(t.Context(), search.PlanLeg{Symbol: &oldLeg})
	testutil.FailErr(t, "search old symbol after overwrite", err)
	if len(afterOld.Hits) != 0 {
		t.Fatalf("stale declaration returned after overwrite: %+v", afterOld.Hits)
	}

	newLeg := *leg
	newLeg.Name = "NewSidecarSym"
	newLeg.Query = search.TextExpr{Text: "NewSidecarSym"}
	afterNew, err := e.Run(t.Context(), search.PlanLeg{Symbol: &newLeg})
	testutil.FailErr(t, "search new symbol after overwrite", err)
	if len(afterNew.Hits) != 1 || afterNew.Hits[0].Title != "NewSidecarSym" || afterNew.Hits[0].Path != "f000.go" || afterNew.Hits[0].RootID != root.ID {
		t.Fatalf("afterNew = %+v, want NewSidecarSym at %s/f000.go", afterNew, root.ID)
	}
}

func TestSymbolExecutorInvalidatesPendingProgressOnRestoredMtimeSameSizeEdit(t *testing.T) {
	e, p, leg := symbolProgressFixture(t, 60)
	f0 := filepath.Join(p.Roots[0].Path, "f000.go")
	origInfo, err := os.Stat(f0)
	testutil.FailErr(t, "stat original f000.go", err)
	origMod := origInfo.ModTime()

	first, err := e.Run(t.Context(), search.PlanLeg{Symbol: leg})
	testutil.FailErr(t, "first pending slice", err)
	if len(first.Hits) != 48 {
		t.Fatalf("first hits = %d, want 48 pending slice", len(first.Hits))
	}

	// Overwrite f000.go with the EXACT same byte length (27 bytes) and restore
	// the exact original mtime without calling repochange.Advance.
	replacement := []byte("package p\nfunc Others() {}\n")
	if int64(len(replacement)) != origInfo.Size() {
		t.Fatalf("replacement size %d != original size %d", len(replacement), origInfo.Size())
	}
	testutil.FailErr(t, "overwrite f000.go", os.WriteFile(f0, replacement, 0o600))
	testutil.FailErr(t, "restore original mtime", os.Chtimes(f0, origMod, origMod))

	second, err := e.Run(t.Context(), search.PlanLeg{Symbol: leg})
	testutil.FailErr(t, "second slice after restored-mtime same-size edit", err)
	for _, hit := range second.Hits {
		if hit.Path == "f000.go" {
			t.Fatalf("stale pending match survived restored-mtime same-size edit: %+v", hit)
		}
	}
	if len(second.Hits) != 48 {
		t.Fatalf("expected pending progress to restart at 48 hits, got %d", len(second.Hits))
	}
}

func TestSymbolExecutorInvalidatesEditBetweenOutlineExtractionAndRetention(t *testing.T) {
	e, p, leg := symbolProgressFixture(t, 60)
	f0 := filepath.Join(p.Roots[0].Path, "f000.go")
	origInfo, err := os.Stat(f0)
	testutil.FailErr(t, "stat original f000.go", err)
	origMod := origInfo.ModTime()

	// Deterministically mutate f000.go AFTER outline extraction has read v1
	// and BEFORE searchProject retains the pending progress entry. Because the
	// digest is bound to the exact source read at outline extraction, retention
	// cannot adopt v2 as the version of v1's extracted declaration.
	mutated := false
	e.beforeRetain = func() {
		if mutated {
			return
		}
		mutated = true
		replacement := []byte("package p\nfunc Others() {}\n")
		testutil.FailErr(t, "overwrite f000.go before retention", os.WriteFile(f0, replacement, 0o600))
		testutil.FailErr(t, "restore mtime before retention", os.Chtimes(f0, origMod, origMod))
	}

	first, err := e.Run(t.Context(), search.PlanLeg{Symbol: leg})
	testutil.FailErr(t, "first pending slice", err)
	if !mutated {
		t.Fatal("expected beforeRetain hook to fire")
	}
	if len(first.Hits) != 48 {
		t.Fatalf("first hits = %d, want 48 pending slice", len(first.Hits))
	}

	// Next call must NOT reuse the stale f000.go declaration extracted before
	// the edit, even though the file was edited before retention and has the
	// same byte size and mtime.
	second, err := e.Run(t.Context(), search.PlanLeg{Symbol: leg})
	testutil.FailErr(t, "second slice after mid-retention edit", err)
	for _, hit := range second.Hits {
		if hit.Path == "f000.go" {
			t.Fatalf("stale match adopted newer version between outline extraction and retention: %+v", hit)
		}
	}
	if len(second.Hits) != 48 {
		t.Fatalf("expected pending progress to restart at 48 hits, got %d", len(second.Hits))
	}
}
