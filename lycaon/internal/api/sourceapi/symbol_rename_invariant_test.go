package sourceapi

import (
	"os"
	"path/filepath"
	"testing"
	"time"

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

func TestSymbolExecutorInvalidatesPendingProgressOnUnwatchedFileOverwrite(t *testing.T) {
	e, p, leg := symbolProgressFixture(t, 60)
	first, err := e.Run(t.Context(), search.PlanLeg{Symbol: leg})
	testutil.FailErr(t, "first pending slice", err)
	if len(first.Hits) != 48 {
		t.Fatalf("first hits = %d, want 48 pending slice", len(first.Hits))
	}

	// Overwrite f000.go while progress is pending, advancing mtime explicitly
	// without calling repochange.Advance, and verify stale f000.go is invalidated.
	f0 := filepath.Join(p.Roots[0].Path, "f000.go")
	testutil.FailErr(t, "overwrite f000.go", os.WriteFile(f0, []byte("package p\nfunc Others() {}\n"), 0o600))
	future := time.Now().Add(2 * time.Second)
	testutil.FailErr(t, "bump mtime", os.Chtimes(f0, future, future))

	second, err := e.Run(t.Context(), search.PlanLeg{Symbol: leg})
	testutil.FailErr(t, "second slice after overwrite", err)
	for _, hit := range second.Hits {
		if hit.Path == "f000.go" {
			t.Fatalf("stale pending match survived overwrite: %+v", hit)
		}
	}
	if len(second.Hits) != 48 {
		t.Fatalf("expected pending progress to restart at 48 hits, got %d", len(second.Hits))
	}
}
