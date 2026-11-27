package sourceapi

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/project/projecttest"
	"github.com/lycaon/lycaon/internal/search"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	catalogtest "github.com/lycaon/lycaon/internal/testsetup/sourcecatalog"
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
	// Surgical invalidation drops & re-outlines only f000.go while preserving
	// the 47 clean matches from slice 1 and finishing the remaining 12 files.
	if len(second.Hits) != 59 || len(second.Issues) != 0 {
		t.Fatalf("second = %d hits, issues=%+v; want 59 fresh hits and 0 issues", len(second.Hits), second.Issues)
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
	// First response explicitly declares pending/incomplete freshness.
	if len(first.Hits) != 48 || len(first.Issues) == 0 {
		t.Fatalf("first = %d hits, issues=%+v; want 48 hits with explicit incomplete issue", len(first.Hits), first.Issues)
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
	if len(second.Hits) != 59 || len(second.Issues) != 0 {
		t.Fatalf("second = %d hits, issues=%+v; want 59 fresh hits and 0 issues", len(second.Hits), second.Issues)
	}
}

func TestSymbolExecutorContinuesAcrossThreeSlicesWithBoundedRevalidation(t *testing.T) {
	// 120 distinct matching files with Cap=200 spans 3 slices (48 + 48 + 24),
	// proving eventual continuation never resets or alternates when >48 files
	// contribute retained matches, while keeping reread bytes bounded.
	e, _, leg := symbolProgressFixture(t, 120)
	var revalFiles []int
	var revalBytes []int64
	e.onRevalidated = func(files int, bytes int64) {
		revalFiles = append(revalFiles, files)
		revalBytes = append(revalBytes, bytes)
	}

	s1, err := e.Run(t.Context(), search.PlanLeg{Symbol: leg})
	testutil.FailErr(t, "slice 1", err)
	if len(s1.Hits) != 48 || len(s1.Issues) == 0 {
		t.Fatalf("slice 1 = %d hits, issues=%+v; want 48 hits + symbol_pending", len(s1.Hits), s1.Issues)
	}

	s2, err := e.Run(t.Context(), search.PlanLeg{Symbol: leg})
	testutil.FailErr(t, "slice 2", err)
	if len(s2.Hits) != 96 || len(s2.Issues) == 0 {
		t.Fatalf("slice 2 = %d hits, issues=%+v; want 96 hits + symbol_pending", len(s2.Hits), s2.Issues)
	}

	s3, err := e.Run(t.Context(), search.PlanLeg{Symbol: leg})
	testutil.FailErr(t, "slice 3", err)
	if len(s3.Hits) != 120 || len(s3.Issues) != 0 {
		t.Fatalf("slice 3 = %d hits, issues=%+v; want 120 hits and 0 issues", len(s3.Hits), s3.Issues)
	}

	// Verify bounded revalidation work: slice 1 does 0 rereads; each resumed
	// slice stays strictly within symbolRevalidateByteLimit.
	if len(revalFiles) != 3 || revalFiles[0] != 0 || revalFiles[1] != 48 || revalFiles[2] != 96 {
		t.Fatalf("revalFiles = %v, want [0 48 96]", revalFiles)
	}
	for i, b := range revalBytes {
		if b > symbolRevalidateByteLimit {
			t.Fatalf("slice %d reread %d bytes > limit %d", i+1, b, symbolRevalidateByteLimit)
		}
	}
}

func TestSymbolExecutorContinuesUnderValidationBytePressure(t *testing.T) {
	// 60 realistic ~64 KiB Go files (~3.84 MiB total, exceeding the 2 MiB
	// pre-read validation allowance). Proves:
	//  1. Pre-read byte budget is strictly enforced (rereadBytes <= 2 MiB).
	//  2. Deferred files are NOT prepended to pending, so slice 2 makes forward
	//     progress into all tail candidates f048.go..f059.go (no prefix starvation).
	//  3. Omitted/deferred files are reported explicitly via a scoped symbol_budget
	//     issue carrying the exact deferred count and byte limit.
	root := t.TempDir()
	pad := "// " + strings.Repeat("x", 64*1024) + "\n"
	for i := range 60 {
		body := "package p\nfunc Target() {}\n" + pad
		testutil.FailErr(t, "write padded source", os.WriteFile(filepath.Join(root, fmt.Sprintf("f%03d.go", i)), []byte(body), 0o600))
	}
	registry := projecttest.NewRegistry(t)
	p, err := projecttest.CreateWithRoot(t.Context(), registry, root)
	testutil.FailErr(t, "create project", err)
	testutil.FailErr(t, "prepare index", catalogtest.AwaitIndex(t.Context(), sourcecatalog.Process(), p.ID, sourcecatalog.Root{ID: p.Roots[0].ID, Path: p.Roots[0].Path}))
	leg := &search.SymbolPlanLeg{
		Name:  "Target",
		Query: search.TextExpr{Text: "Target"},
		Roots: []search.CodeRoot{{ProjectID: p.ID, RootID: p.Roots[0].ID, Path: p.Roots[0].Path}},
		Cap:   200,
		Flags: search.MatchFlags{WholeWord: true},
	}
	e := NewSymbolExecutor(registry)
	e.legBudget = unboundedSymbolLegBudget
	var revalFiles []int
	var revalBytes []int64
	e.onRevalidated = func(files int, bytes int64) {
		revalFiles = append(revalFiles, files)
		revalBytes = append(revalBytes, bytes)
	}

	s1, err := e.Run(t.Context(), search.PlanLeg{Symbol: leg})
	testutil.FailErr(t, "slice 1", err)
	if len(s1.Hits) != 48 || len(s1.Issues) == 0 {
		t.Fatalf("slice 1 = %d hits, issues=%+v; want 48 hits + symbol_pending", len(s1.Hits), s1.Issues)
	}

	s2, err := e.Run(t.Context(), search.PlanLeg{Symbol: leg})
	testutil.FailErr(t, "slice 2", err)
	if len(revalBytes) != 2 || revalBytes[1] > symbolRevalidateByteLimit {
		t.Fatalf("slice 2 exceeded pre-read validation budget: revalBytes=%v limit=%d", revalBytes, symbolRevalidateByteLimit)
	}
	if revalFiles[1] == 0 || revalFiles[1] >= 48 {
		t.Fatalf("expected partial validation under 2 MiB pressure (0 < n < 48), got revalFiles=%v", revalFiles)
	}
	// Verify every tail candidate f048.go..f059.go was outlined and returned.
	seen := make(map[string]bool, len(s2.Hits))
	for _, h := range s2.Hits {
		seen[h.Path] = true
	}
	for i := 48; i < 60; i++ {
		want := fmt.Sprintf("f%03d.go", i)
		if !seen[want] {
			t.Fatalf("tail candidate %s was starved under validation byte pressure (hits=%d)", want, len(s2.Hits))
		}
	}
	// Verify explicit scoped symbol_budget gap accounts for deferred files.
	wantDeferred := 48 - revalFiles[1]
	foundGap := false
	for _, iss := range s2.Issues {
		if iss.Reason == search.IssueReason("symbol_budget") && iss.Count == wantDeferred && iss.Limit == int(symbolRevalidateByteLimit) {
			foundGap = true
		}
	}
	if !foundGap {
		t.Fatalf("expected scoped symbol_budget gap (count=%d limit=%d), got issues=%+v", wantDeferred, symbolRevalidateByteLimit, s2.Issues)
	}
}
