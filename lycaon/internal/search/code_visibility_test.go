package search

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCodeSearchExplicitPathsAdmitHiddenFiles(t *testing.T) {
	root := t.TempDir()
	for _, rel := range []string{"visible.txt", ".hidden/target.txt", ".other/target.txt"} {
		abs := filepath.Join(root, rel)
		testutil.FailErr(t, "create parent", os.MkdirAll(filepath.Dir(abs), 0o755))
		testutil.FailErr(t, "write search fixture", os.WriteFile(abs, []byte("visibility_needle\n"), 0o600))
	}
	for _, tc := range []struct {
		name  string
		query Node
		flags MatchFlags
		want  string
	}{
		{name: "default", query: TextExpr{Text: "visibility_needle"}, want: "visible.txt"},
		{name: "explicit path", query: AndExpr{Exprs: []Node{TextExpr{Text: "visibility_needle"}, FilterExpr{Field: "path", Value: ".hidden/"}}}, want: ".hidden/target.txt"},
		{name: "include glob", query: TextExpr{Text: "visibility_needle"}, flags: MatchFlags{Include: []string{".hidden/**"}}, want: ".hidden/target.txt"},
		{name: "negative path", query: AndExpr{Exprs: []Node{TextExpr{Text: "visibility_needle"}, NotExpr{Expr: FilterExpr{Field: "path", Value: ".other/"}}}}, want: "visible.txt"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hits := runCodeLeg(t, &CodePlanLeg{Query: tc.query, Flags: tc.flags,
				PathRoots: []CodeRoot{{ProjectID: "p", RootID: "root", Path: root}}, Lines: true})
			if len(hits) != 1 || hits[0].Path != tc.want {
				t.Fatalf("hits = %+v, want %s", hits, tc.want)
			}
		})
	}
}

func TestSymbolDependencyAdmissionUsesTheDeclarationRoot(t *testing.T) {
	root := t.TempDir()
	other := t.TempDir()
	for _, rel := range []string{".cache/generated/item.go", ".private/item.go", ".claude/worktrees/job/item.go"} {
		testutil.FailErr(t, "create fixture parent", os.MkdirAll(filepath.Dir(filepath.Join(root, rel)), 0o755))
		testutil.FailErr(t, "write fixture", os.WriteFile(filepath.Join(root, rel), []byte("package fixture\nfunc DependencyTarget() {}\n"), 0o600))
	}
	testutil.FailErr(t, "mark nested checkout", os.WriteFile(filepath.Join(root, ".claude/worktrees/job/.git"), []byte("gitdir: /fixture/gitdir\n"), 0o600))
	catalog := sourcecatalog.New()
	plan := compileSymbolPlan(t, "DependencyTarget", MatchFlags{}, BudgetComplete)
	filter, err := CompileSymbolFilter(plan.Symbol)
	testutil.FailErr(t, "compile declaration filter", err)
	for _, tc := range []struct {
		root, path    string
		include, want bool
	}{
		{root, ".cache/generated/item.go", true, true},
		{root, ".claude/worktrees/job/item.go", true, true},
		{root, ".private/item.go", true, false},
		{root, ".cache/generated/item.go", false, false},
		{other, ".claude/worktrees/job/item.go", true, false},
	} {
		got := filter.ForRoot(t.Context(), catalog, tc.root, tc.include).Admits(tc.path, "DependencyTarget")
		if got != tc.want {
			t.Errorf("root=%s path=%s dependencies=%t: admitted=%t want=%t", tc.root, tc.path, tc.include, got, tc.want)
		}
	}
}
