package symbolsearch

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestSymbolProgressRetainsPendingOutlines(t *testing.T) {
	files := map[string]string{}
	for i := range OutlineFileCap*2 + 3 {
		files[fmt.Sprintf("f%03d.go", i)] = "package p\nfunc Target() {}\n"
	}
	p := symbolSearchFixture(t, files)
	hits := make([]project.DeclarationSearchHit, 0, len(files))
	for i := range len(files) {
		hits = append(hits, project.DeclarationSearchHit{RootID: p.Roots[0].ID, Path: fmt.Sprintf("f%03d.go", i)})
	}
	calls := 0
	discover := func(context.Context, project.DeclarationSearchQuery) ([]project.DeclarationSearchHit, project.DeclarationCoverage, error) {
		calls++
		return hits, project.DeclarationCoverage{}, nil
	}
	state := &Progress{}
	req := Request{Query: "Target", Exact: true, Limit: 200, Progress: state, Wall: testSymbolWall, OutlineWall: testSymbolWall}
	for attempt, want := range []int{48, 96, 99} {
		result, err := Run(t.Context(), p, req, discover)
		testutil.FailErr(t, "advance symbols", err)
		if len(result.Symbols) != want || result.Incomplete != (attempt < 2) {
			t.Fatalf("attempt %d: %d symbols, incomplete=%v", attempt, len(result.Symbols), result.Incomplete)
		}
	}
	if calls != 1 {
		t.Fatalf("discovery calls=%d, want one retained batch", calls)
	}
}

func TestSymbolProgressCompleteRefinementReopensAbbreviations(t *testing.T) {
	p := symbolSearchFixture(t, map[string]string{"a.go": "package p\nfunc CfgOne() {}\nfunc CfgTwo() {}\n", "b.go": "package p\nfunc CacheFileGroup() {}\n"})
	state := &Progress{}
	req := Request{Query: "cfg", Limit: 2, Progress: state, Wall: testSymbolWall, OutlineWall: testSymbolWall}
	first, err := Run(t.Context(), p, req, testDeclarationSearch)
	testutil.FailErr(t, "interactive symbols", err)
	if !first.Limited || first.Incomplete {
		t.Fatalf("interactive=%+v", first)
	}
	req.Limit = 200
	second, err := Run(t.Context(), p, req, testDeclarationSearch)
	testutil.FailErr(t, "complete symbols", err)
	if len(second.Symbols) != 3 || second.Incomplete || second.Limited {
		t.Fatalf("complete=%+v", second)
	}
}

func TestSymbolProgressPreservesCoverageReasons(t *testing.T) {
	p := symbolSearchFixture(t, map[string]string{"a.go": "package p\nfunc Target() {}\n"})
	state := &Progress{}
	discover := func(context.Context, project.DeclarationSearchQuery) ([]project.DeclarationSearchHit, project.DeclarationCoverage, error) {
		return nil, project.DeclarationCoverage{Gaps: []project.DeclarationGap{{Reason: project.DeclarationCatalogWarming, Count: 2}, {Reason: "catalog_bounded", Count: 3}}}, nil
	}
	result, err := Run(t.Context(), p, Request{Query: "Target", Progress: state}, discover)
	testutil.FailErr(t, "coverage search", err)
	if !result.Incomplete {
		t.Fatal("coverage gap reported complete")
	}
	counts := map[project.DeclarationGapReason]int{}
	for _, gap := range result.Coverage.Gaps {
		counts[gap.Reason] = gap.Count
	}
	if counts[project.DeclarationCatalogWarming] != 2 || counts["catalog_bounded"] != 3 {
		t.Fatalf("coverage=%+v", result.Coverage)
	}
}

func TestSymbolProgressCancellationKeepsNominatedFiles(t *testing.T) {
	p := symbolSearchFixture(t, map[string]string{"a.go": "package p\nfunc Target() {}\n"})
	state := &Progress{}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	calls := 0
	discover := func(context.Context, project.DeclarationSearchQuery) ([]project.DeclarationSearchHit, project.DeclarationCoverage, error) {
		calls++
		if calls == 1 {
			cancel()
			return []project.DeclarationSearchHit{{RootID: p.Roots[0].ID, Path: "a.go"}}, project.DeclarationCoverage{Gaps: []project.DeclarationGap{{Reason: project.DeclarationTimeBudget}}}, nil
		}
		return nil, project.DeclarationCoverage{}, nil
	}
	req := Request{Query: "Target", Exact: true, Progress: state, Wall: testSymbolWall, OutlineWall: testSymbolWall}
	canceled, err := Run(ctx, p, req, discover)
	testutil.FailErr(t, "canceled search", err)
	if !canceled.Incomplete || len(canceled.Symbols) != 0 {
		t.Fatalf("canceled=%+v", canceled)
	}
	resumed, err := Run(t.Context(), p, req, discover)
	testutil.FailErr(t, "resume outlines", err)
	if len(resumed.Symbols) != 1 || calls != 1 {
		t.Fatalf("resumed=%+v calls=%d", resumed, calls)
	}
	done, err := Run(t.Context(), p, req, discover)
	testutil.FailErr(t, "complete discovery", err)
	if done.Incomplete || len(done.Symbols) != 1 {
		t.Fatalf("completion=%+v", done)
	}
}

func TestSymbolProgressBoundsRetainedDeclarationBytes(t *testing.T) {
	run := &symbolSearchRun{matches: []Match{{Name: strings.Repeat("x", retainedDeclarationBytes+1), Path: "large.go"}}}
	state := &Progress{}
	run.saveProgress(state)
	if len(state.matches) != 0 || len(state.gaps) != 1 || state.gaps[0].Reason != project.DeclarationSymbolBudget {
		t.Fatalf("retained %d matches; gaps=%+v", len(state.matches), state.gaps)
	}
}
