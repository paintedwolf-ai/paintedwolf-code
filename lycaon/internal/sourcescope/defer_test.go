package sourcescope

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCatalogPriorityVisitsIgnoredDirectoriesLastAndSpendsTheBudgetOnSource(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, ".gitignore", "build/\n")
	for i := range 6 {
		writeFile(t, root, "build/out"+string(rune('a'+i)), "")
	}
	writeFile(t, root, "src/a.go", "")
	writeFile(t, root, "src/b.go", "")
	writeFile(t, root, "zz/c.go", "")

	// The budget fits all source entries and three of six build files.
	plane := Plane{DeferIgnored: true, Budgets: sandbox.SurveyBudgets{DirectoryEntries: 1000, SubtreeEntries: 1000, WalkEntries: 10}}
	scope := New(root, Options{Plane: plane})
	var visited []string
	var boundaries []sandbox.SurveyBoundary
	opts := scope.SurveyOptions(sandbox.SurveyOptions{IncludeHidden: true})
	opts.OnBoundary = func(b sandbox.SurveyBoundary) { boundaries = append(boundaries, b) }
	err := sandbox.SurveyWalk(context.Background(), root, opts, func(e sandbox.SurveyEntry) (sandbox.SurveyAction, error) {
		visited = append(visited, e.Rel)
		return sandbox.SurveyContinue, nil
	})
	testutil.FailErr(t, "survey", err)
	for _, want := range []string{"src/a.go", "src/b.go", "zz/c.go", "build"} {
		if !contains(visited, want) {
			t.Fatalf("missing %s in %v", want, visited)
		}
	}
	underBuild := 0
	for _, rel := range visited {
		if len(rel) > 6 && rel[:6] == "build/" {
			underBuild++
		}
	}
	if underBuild == 0 || underBuild == 6 {
		t.Fatalf("the ignored tree is indexed as far as the budget allows, not excluded: %d of 6 under build/ (%v)", underBuild, visited)
	}
	if len(boundaries) != 1 || boundaries[0].Reason != sandbox.BoundaryWalkBudget {
		t.Fatalf("boundaries = %+v, want one walk_budget cut", boundaries)
	}
}

func TestCatalogScopeNeverPrunesByIgnoreFile(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, ".gitignore", "vendor/\n")
	writeFile(t, root, "vendor/dep.go", "")
	scope := New(root, Options{Plane: Plane{DeferIgnored: true, Budgets: capturePlane().Budgets}})
	if _, prune := scope.PruneDir("vendor", ""); prune {
		t.Fatal("priority must never become exclusion")
	}
	if !scope.DeferDir("vendor", "") || scope.DeferDir("src", "") {
		t.Fatal("only the ignored directory is deferred")
	}
}
