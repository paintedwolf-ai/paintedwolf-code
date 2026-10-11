package sourceapi

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/search"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	catalogtest "github.com/lycaon/lycaon/internal/testsetup/sourcecatalog"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestBoundedSymbolSemanticsMatchReferenceBeforeCapacity(t *testing.T) {
	cases := []struct {
		name  string
		query search.Node
		want  []string
	}{
		{"negation", search.AndExpr{Exprs: []search.Node{search.TextExpr{Text: "Target"}, search.NotExpr{Expr: search.OrExpr{Exprs: []search.Node{search.TextExpr{Text: "Excluded"}, search.TextExpr{Text: "Other"}}}}}}, []string{"TargetAllowed"}},
		{"nested and or", search.AndExpr{Exprs: []search.Node{search.TextExpr{Text: "Target"}, search.OrExpr{Exprs: []search.Node{search.FilterExpr{Field: "path", Value: "f001.go"}, search.AndExpr{Exprs: []search.Node{search.FilterExpr{Field: "path", Value: "f000.go"}, search.NotExpr{Expr: search.TextExpr{Text: "Excluded"}}}}}}}}, []string{"TargetAllowed", "TargetOther"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, p, leg := symbolProgressFixture(t, 2)
			var body strings.Builder
			body.WriteString("package p\n")
			for i := range 201 {
				fmt.Fprintf(&body, "func TargetExcluded%03d() {}\n", i)
			}
			body.WriteString("func TargetAllowed() {}\n")
			replaceSymbolInput(t, p.Roots[0].Path, "f000.go", body.String())
			replaceSymbolInput(t, p.Roots[0].Path, "f001.go", "package p\nfunc TargetOther() {}\n")
			leg.Query, leg.Flags.WholeWord, leg.Cap = tc.query, false, 3
			for _, budget := range []search.SearchBudget{search.BudgetInteractive, search.BudgetComplete} {
				leg.Budget = budget
				result, err := e.Run(t.Context(), search.PlanLeg{Symbol: leg})
				testutil.FailErr(t, "bounded reference search", err)
				names := symbolHitNames(result)
				if !reflect.DeepEqual(names, tc.want) || len(result.Issues) != 0 {
					t.Fatalf("budget=%s names=%v want=%v issues=%+v", budget, names, tc.want, result.Issues)
				}
			}
		})
	}
}

func TestSymbolCancellationPreservesProgressAndContentChangeInvalidatesIt(t *testing.T) {
	e, p, leg := symbolProgressFixture(t, 60)
	first, err := e.Run(t.Context(), search.PlanLeg{Symbol: leg})
	testutil.FailErr(t, "start bounded query", err)
	if len(first.Hits) >= 60 {
		t.Fatal("fixture did not cross an outline bound")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = e.Run(ctx, search.PlanLeg{Symbol: leg})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation=%v", err)
	}
	complete, err := e.Run(t.Context(), search.PlanLeg{Symbol: leg})
	testutil.FailErr(t, "resume canceled query", err)
	if len(complete.Hits) != 60 || len(complete.Issues) != 0 {
		t.Fatalf("cancellation discarded progress: %+v", complete)
	}
	replaceSymbolInput(t, p.Roots[0].Path, "f000.go", "package p\nfunc Replacement() {}\n")
	for attempt := range 4 {
		changed, err := e.Run(t.Context(), search.PlanLeg{Symbol: leg})
		testutil.FailErr(t, "search changed content", err)
		for _, hit := range changed.Hits {
			if hit.Path == "f000.go" {
				t.Fatalf("attempt %d retained invalid declaration: %+v", attempt, hit)
			}
		}
		if len(changed.Issues) == 0 {
			if len(changed.Hits) != 59 {
				t.Fatalf("changed reference count=%d want=59", len(changed.Hits))
			}
			return
		}
	}
	t.Fatal("changed query never completed")
}

func replaceSymbolInput(t *testing.T, root, name, body string) {
	t.Helper()
	testutil.FailErr(t, "replace declaration input", os.WriteFile(filepath.Join(root, name), []byte(body), 0600))
	repochange.Advance(root)
	sourcecatalog.Process().InvalidateRootChange(root, []string{name}, repochange.StructuralPathSet{})
}

func symbolHitNames(result search.ExecutorReport) []string {
	names := make([]string, 0, len(result.Hits))
	for _, hit := range result.Hits {
		names = append(names, hit.Title)
	}
	sort.Strings(names)
	return names
}

func TestSymbolRootAndDependencyScopesRefineToReference(t *testing.T) {
	e, p, leg := symbolProgressFixture(t, 2)
	root := p.Roots[0]
	testutil.FailErr(t, "create dependency fixture", os.MkdirAll(filepath.Join(root.Path, "node_modules/lib"), 0700))
	replaceSymbolInput(t, root.Path, "node_modules/lib/dependency.go", "package lib\nfunc TargetDependency() {}\n")
	sourcecatalog.Process().InvalidateRoot(root.Path, "node_modules/lib/dependency.go")
	testutil.FailErr(t, "settle dependency membership", catalogtest.AwaitIndex(t.Context(), sourcecatalog.Process(), p.ID, sourcecatalog.Root{ID: root.ID, Path: root.Path}))
	second := t.TempDir()
	testutil.FailErr(t, "seed second root", os.WriteFile(filepath.Join(second, "other.go"), []byte("package p\nfunc TargetOtherRoot() {}\n"), 0600))
	change, err := e.registry.AttachRoot(t.Context(), p.ID, project.AttachRootParams{Path: second})
	testutil.FailErr(t, "attach reference root", err)
	attached := change.Added
	if attached == nil {
		t.Fatal("root fixture not attached")
	}
	testutil.FailErr(t, "settle attached root", catalogtest.AwaitIndex(t.Context(), sourcecatalog.Process(), p.ID, sourcecatalog.Root{ID: attached.ID, Path: attached.Path}))
	leg.Flags.WholeWord = false
	leg.ExcludeDirs = []string{"node_modules"}
	for _, tc := range []struct {
		name         string
		roots        []search.CodeRoot
		dependencies bool
		want         int
	}{
		{"one root excludes dependencies", leg.Roots, false, 2},
		{"one root includes dependencies", leg.Roots, true, 3},
		{"second root", []search.CodeRoot{{ProjectID: p.ID, RootID: attached.ID, Path: attached.Path}}, false, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			leg.Roots, leg.IncludeDependencies = tc.roots, tc.dependencies
			leg.ExcludeDirs = []string{"node_modules"}
			if tc.dependencies {
				leg.ExcludeDirs = nil
			}
			// Refinement increases the result capacity without changing admission.
			leg.Budget, leg.Cap = search.BudgetInteractive, 1
			bounded, err := e.Run(t.Context(), search.PlanLeg{Symbol: leg})
			testutil.FailErr(t, "interactive scoped search", err)
			if len(bounded.Hits) != 1 {
				t.Fatalf("bounded scoped hits=%d issues=%+v", len(bounded.Hits), bounded.Issues)
			}
			leg.Budget, leg.Cap = search.BudgetComplete, 200
			reference, err := e.Run(t.Context(), search.PlanLeg{Symbol: leg})
			testutil.FailErr(t, "refine scoped search", err)
			if len(reference.Hits) != tc.want || len(reference.Issues) != 0 {
				t.Fatalf("scoped reference=%d want=%d issues=%+v", len(reference.Hits), tc.want, reference.Issues)
			}
			for _, hit := range bounded.Hits {
				admitted := false
				for _, candidate := range reference.Hits {
					admitted = admitted || candidate.ID == hit.ID
				}
				if !admitted {
					t.Fatalf("bounded result absent from complete reference: %+v", hit)
				}
			}
		})
	}
}
