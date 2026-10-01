package sandbox

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

type recordingScope struct {
	pruned map[string]string
	files  map[string]bool
}

func (s recordingScope) PruneDir(rel, _ string) (string, bool) {
	detail, ok := s.pruned[rel]
	return detail, ok
}

func (s recordingScope) AdmitFile(rel, _ string) bool {
	if s.files == nil {
		return true
	}
	return !s.files[rel]
}

func surveyWithBoundaries(t *testing.T, root string, opts SurveyOptions) ([]string, []SurveyBoundary) {
	t.Helper()
	var boundaries []SurveyBoundary
	opts.OnBoundary = func(b SurveyBoundary) { boundaries = append(boundaries, b) }
	var visited []string
	err := SurveyWalk(context.Background(), root, opts, func(e SurveyEntry) (SurveyAction, error) {
		visited = append(visited, e.Rel)
		return SurveyContinue, nil
	})
	if err != nil {
		t.Fatalf("SurveyWalk: %v", err)
	}
	sort.Strings(visited)
	return visited, boundaries
}

func TestSurveyWalkDirectoryCapDeclinesWideDirectoryWithoutEnteringIt(t *testing.T) {
	dir := t.TempDir()
	files := []string{"src/a.go", "src/b.go"}
	for i := range 6 {
		files = append(files, fmt.Sprintf("wide/f%d", i))
	}
	writeTree(t, dir, files)

	visited, boundaries := surveyWithBoundaries(t, dir, SurveyOptions{Budgets: SurveyBudgets{DirectoryEntries: 4}})
	for _, rel := range visited {
		if strings.HasPrefix(rel, "wide/") {
			t.Fatalf("a directory over the cap must not be entered; visited %v", visited)
		}
	}
	if !contains(visited, "wide") || !contains(visited, "src/a.go") {
		t.Fatalf("siblings and the wide directory itself stay visible; visited %v", visited)
	}
	if len(boundaries) != 1 || boundaries[0].Rel != "wide" || boundaries[0].Reason != BoundaryDirectoryCap || boundaries[0].Entries != 5 {
		t.Fatalf("boundary = %+v, want wide/directory_cap with cap+1 entries", boundaries)
	}
}

func TestSurveyWalkSubtreeCapCutsOneSubtreeAndContinuesBesideIt(t *testing.T) {
	dir := t.TempDir()
	var files []string
	for i := range 10 {
		files = append(files, fmt.Sprintf("big/d%d/f", i))
	}
	files = append(files, "small/x.go", "zlast/y.go")
	writeTree(t, dir, files)

	visited, boundaries := surveyWithBoundaries(t, dir, SurveyOptions{Budgets: SurveyBudgets{SubtreeEntries: 6}})
	if !contains(visited, "small/x.go") || !contains(visited, "zlast/y.go") {
		t.Fatalf("siblings after the cut subtree must still be surveyed; visited %v", visited)
	}
	under := 0
	for _, rel := range visited {
		if strings.HasPrefix(rel, "big/") {
			under++
		}
	}
	if under == 0 || under > 6 {
		t.Fatalf("the cut subtree keeps what was read up to the cap, got %d entries under big/", under)
	}
	if len(boundaries) != 1 || boundaries[0].Rel != "big" || boundaries[0].Reason != BoundarySubtreeCap {
		t.Fatalf("boundary = %+v, want big/subtree_cap", boundaries)
	}
}

func TestSurveyWalkWalkBudgetEndsTheTraversalAtTheRoot(t *testing.T) {
	dir := t.TempDir()
	var files []string
	for i := range 12 {
		files = append(files, fmt.Sprintf("d%02d/f", i))
	}
	writeTree(t, dir, files)

	visited, boundaries := surveyWithBoundaries(t, dir, SurveyOptions{Budgets: SurveyBudgets{WalkEntries: 5}})
	if len(visited) > 5 {
		t.Fatalf("walk budget of 5 entries visited %d: %v", len(visited), visited)
	}
	if len(boundaries) != 1 || boundaries[0].Rel != "." || boundaries[0].Reason != BoundaryWalkBudget {
		t.Fatalf("boundary = %+v, want ./walk_budget", boundaries)
	}
}

func TestSurveyWalkScopePrunesDirectoriesAndFilesWithReasons(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, []string{"src/a.go", "out/b.js", "src/secret.env"})

	scope := recordingScope{pruned: map[string]string{"out": "ignored"}, files: map[string]bool{"src/secret.env": true}}
	visited, boundaries := surveyWithBoundaries(t, dir, SurveyOptions{Scope: scope})
	if contains(visited, "out") || contains(visited, "out/b.js") || contains(visited, "src/secret.env") {
		t.Fatalf("scope-declined entries must not be visited: %v", visited)
	}
	if !contains(visited, "src/a.go") {
		t.Fatalf("admitted entries survive: %v", visited)
	}
	if len(boundaries) != 1 || boundaries[0].Rel != "out" || boundaries[0].Reason != BoundaryScope || boundaries[0].Detail != "ignored" {
		t.Fatalf("boundary = %+v, want out/scope/ignored", boundaries)
	}
}

func TestSurveyWalkWithoutScopeShowsEverything(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, []string{".gitignore", "node_modules/dep/index.js", "build/out.o", "src/a.go"})
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("node_modules\nbuild\n"), 0o644); err != nil {
		t.Fatalf("write gitignore: %v", err)
	}

	visited, boundaries := surveyWithBoundaries(t, dir, SurveyOptions{IncludeHidden: true})
	if !contains(visited, "node_modules/dep/index.js") || !contains(visited, "build/out.o") {
		t.Fatalf("a survey without a scope never consults ignore files: %v", visited)
	}
	if len(boundaries) != 0 {
		t.Fatalf("no bounds were set, boundaries = %+v", boundaries)
	}
}

func TestSurveyWalkVisitsEntriesInNameOrder(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, []string{"b.go", "a.go", "c/z.go", "c/y.go"})

	var order []string
	err := SurveyWalk(context.Background(), dir, SurveyOptions{}, func(e SurveyEntry) (SurveyAction, error) {
		order = append(order, e.Rel)
		return SurveyContinue, nil
	})
	if err != nil {
		t.Fatalf("SurveyWalk: %v", err)
	}
	want := []string{"a.go", "b.go", "c", "c/y.go", "c/z.go"}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Fatalf("order = %v, want %v", order, want)
	}
}

func TestWalkBudgetChargesTheShallowestExceededAncestor(t *testing.T) {
	b := NewWalkBudget(SurveyBudgets{SubtreeEntries: 3, WalkEntries: 100})
	if depth := b.EnterDir(); depth != 1 {
		t.Fatalf("root depth = %d, want 1", depth)
	}
	b.EnterDir() // depth 2
	b.EnterDir() // depth 3
	for range 3 {
		if cut, _ := b.Charge(1); cut != -1 {
			t.Fatalf("cap not yet exceeded, cut = %d", cut)
		}
	}
	cut, reason := b.Charge(1)
	if cut != 2 || reason != BoundarySubtreeCap {
		t.Fatalf("cut = %d/%s, want 2/subtree_cap (the shallowest non-root ancestor)", cut, reason)
	}
	if got := b.LeaveDir(); got != 4 {
		t.Fatalf("leaving depth 3 reports %d observed, want 4", got)
	}
}

func contains(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

type deferringScope struct{ deferred map[string]bool }

func (deferringScope) PruneDir(string, string) (string, bool) { return "", false }
func (deferringScope) AdmitFile(string, string) bool          { return true }
func (s deferringScope) DeferDir(rel, _ string) bool          { return s.deferred[rel] }

func TestSurveyWalkVisitsDeferredDirectoriesAfterTheirSiblings(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, []string{"aaa/x", "bbb/y", "ccc/z", "file"})

	var order []string
	err := SurveyWalk(context.Background(), dir, SurveyOptions{Scope: deferringScope{deferred: map[string]bool{"aaa": true}}}, func(e SurveyEntry) (SurveyAction, error) {
		order = append(order, e.Rel)
		return SurveyContinue, nil
	})
	if err != nil {
		t.Fatalf("SurveyWalk: %v", err)
	}
	want := "bbb,bbb/y,ccc,ccc/z,file,aaa,aaa/x"
	if got := strings.Join(order, ","); got != want {
		t.Fatalf("order = %s, want %s", got, want)
	}
}
