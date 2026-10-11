package search

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/backgroundwork"
	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	catalogtest "github.com/lycaon/lycaon/internal/testsetup/sourcecatalog"
	"github.com/lycaon/lycaon/internal/testutil"
)

func candidateFixture(t *testing.T, count int) (*CodeExecutor, *CodePlanLeg) {
	t.Helper()
	root := t.TempDir()
	for i := range count {
		testutil.FailErr(t, "write candidate", os.WriteFile(filepath.Join(root, fmt.Sprintf("%03d.go", i)), []byte("package p\nfunc Target() {}\n// Target Target\n"), 0600))
	}
	c := sourcecatalog.New()
	t.Cleanup(func() { testutil.FailErr(t, "drain catalog", c.Drain(context.Background())) })
	testutil.FailErr(t, "prepare candidates", catalogtest.AwaitIndex(t.Context(), c, "p", sourcecatalog.Root{ID: "r", Path: root}))
	e := NewCodeExecutor(decide.Reranker{})
	e.catalog = c
	return e, &CodePlanLeg{Query: TextExpr{Text: "Target", Phrase: true}, PathRoots: []CodeRoot{{ProjectID: "p", RootID: "r", Path: root}}, Lines: true, Candidates: true, Cap: 2, Progress: &CodeProgress{}, Wall: time.Minute}
}

func TestDeclarationCandidatesAdvanceBoundedFrontier(t *testing.T) {
	e, leg := candidateFixture(t, 5)
	seen := map[string]bool{}
	for attempt, want := range []int{2, 2, 1} {
		report, err := e.Run(t.Context(), PlanLeg{Code: leg})
		testutil.FailErr(t, "advance discovery", err)
		if len(report.Hits) != want || report.Limited != (attempt < 2) {
			t.Fatalf("attempt %d report=%+v", attempt, report)
		}
		for _, hit := range report.Hits {
			if seen[hit.Path] {
				t.Fatalf("revisited %s", hit.Path)
			}
			seen[hit.Path] = true
		}
	}
	if leg.Progress.Root != 1 || len(seen) != 5 {
		t.Fatalf("frontier=%+v seen=%v", leg.Progress, seen)
	}
}

func TestDeclarationCandidatesAdvanceAcrossFreshDependencyReaders(t *testing.T) {
	e, leg := candidateFixture(t, 5)
	leg.IncludeDependencies = true
	seen := map[string]bool{}
	for attempt, want := range []int{2, 2, 1} {
		report, err := e.Run(t.Context(), PlanLeg{Code: leg})
		testutil.FailErr(t, "advance private dependency discovery", err)
		if len(report.Hits) != want || report.Limited != (attempt < 2) || len(report.Issues) != 0 {
			t.Fatalf("attempt %d: fresh private reader stalled source progress: %+v", attempt, report)
		}
		for _, hit := range report.Hits {
			if seen[hit.Path] {
				t.Fatalf("private reader revisited %s", hit.Path)
			}
			seen[hit.Path] = true
		}
	}
	if leg.Progress.Root != 1 || len(seen) != 5 {
		t.Fatalf("private discovery frontier=%+v seen=%v", leg.Progress, seen)
	}
}

func TestDeclarationCandidatesDetectContentEpochChange(t *testing.T) {
	for name, include := range map[string]bool{"shared index": false, "private dependency index": true} {
		t.Run(name, func(t *testing.T) {
			e, leg := candidateFixture(t, 3)
			leg.IncludeDependencies = include
			_, err := e.Run(t.Context(), PlanLeg{Code: leg})
			testutil.FailErr(t, "initial candidates", err)
			before := *leg.Progress
			repochange.Advance(leg.PathRoots[0].Path)
			report, err := e.Run(t.Context(), PlanLeg{Code: leg})
			testutil.FailErr(t, "changed candidates", err)
			if len(report.Hits) != 0 || len(report.Issues) != 1 || report.Issues[0].Reason != IssueCatalogRefreshing || *leg.Progress != before {
				t.Fatalf("changed result=%+v frontier=%+v", report, leg.Progress)
			}
		})
	}
}
func TestDeclarationForegroundCompletionDoesNotRequirePreparation(t *testing.T) {
	e, leg := candidateFixture(t, 2)
	leg.Cap = 10
	release, err := backgroundwork.Process().Acquire(t.Context(), backgroundwork.Request{Lane: leg.PathRoots[0].Path, Resources: []backgroundwork.Resource{backgroundwork.ResourceIO}})
	testutil.FailErr(t, "hold content admission", err)
	defer release()
	report, err := e.Run(t.Context(), PlanLeg{Code: leg})
	testutil.FailErr(t, "foreground candidates", err)
	if len(report.Hits) != 2 || report.Limited || len(report.CoverageIssues(ExecutorSymbol)) != 0 {
		t.Fatalf("foreground=%+v", report)
	}
	// Draining cancels queued preparation even while the admission is held.
	testutil.FailErr(t, "drain blocked preparation", e.catalog.Drain(t.Context()))
}

func TestSymbolBudgetAllocatesEveryPhase(t *testing.T) {
	interactive, complete := BudgetInteractive.SymbolAllocation(), BudgetComplete.SymbolAllocation()
	for budget, allocation := range map[SearchBudget]SymbolAllocation{BudgetInteractive: interactive, BudgetComplete: complete} {
		if allocation.Discovery+allocation.Abbreviation+allocation.Outline != budget.Wall() {
			t.Fatalf("%s allocation=%+v", budget, allocation)
		}
	}
	if complete.Discovery != 5*interactive.Discovery || complete.Abbreviation != 5*interactive.Abbreviation || complete.Outline != 5*interactive.Outline {
		t.Fatalf("allocations: %+v %+v", interactive, complete)
	}
}

func TestDeclarationCandidatesRejectRetiredIndexInstance(t *testing.T) {
	e, leg := candidateFixture(t, 3)
	_, err := e.Run(t.Context(), PlanLeg{Code: leg})
	testutil.FailErr(t, "initial candidate slice", err)
	before := *leg.Progress
	testutil.FailErr(t, "retire catalog", e.catalog.Trees.ClearTreeStores(t.Context(), func() error { return nil }))
	root := leg.PathRoots[0]
	testutil.FailErr(t, "reopen catalog", catalogtest.AwaitIndex(t.Context(), e.catalog, root.ProjectID, sourcecatalog.Root{ID: root.RootID, Path: root.Path}))
	next, err := e.Run(t.Context(), PlanLeg{Code: leg})
	testutil.FailErr(t, "retired continuation", err)
	if len(next.Hits) != 0 || len(next.Issues) != 1 || next.Issues[0].Reason != IssueCatalogRefreshing || *leg.Progress != before {
		t.Fatalf("retired index result=%+v", next)
	}
}

func TestDeclarationCandidatesCanceledRetryKeepsFrontier(t *testing.T) {
	e, leg := candidateFixture(t, 3)
	_, err := e.Run(t.Context(), PlanLeg{Code: leg})
	testutil.FailErr(t, "first candidate slice", err)
	before := *leg.Progress
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = e.Run(ctx, PlanLeg{Code: leg})
	if !errors.Is(err, context.Canceled) || *leg.Progress != before {
		t.Fatalf("canceled scan error=%v frontier=%+v", err, leg.Progress)
	}
	next, err := e.Run(t.Context(), PlanLeg{Code: leg})
	testutil.FailErr(t, "resume discovery", err)
	if len(next.Hits) != 1 || next.Hits[0].Path != "002.go" || next.Limited {
		t.Fatalf("resumed=%+v", next)
	}
}
