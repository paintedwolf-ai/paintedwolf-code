package git_test

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/gitrepo"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
)

func initDiscoverRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	mgr := git.NewManager()
	testutil.FailErr(t, "init", mgr.Init(context.Background(), dir))
	path := filepath.Join(dir, "a.txt")
	testutil.FailErr(t, "write", os.WriteFile(path, []byte("one\n"), 0o644))
	_, commitErr := mgr.Commit(context.Background(), dir, git.GitCommitOpts{Message: "init", Paths: []string{"a.txt"}})
	testutil.FailErr(t, "commit", commitErr)
	return dir
}

func TestDiscoverRepos_twoSeparateCheckouts(t *testing.T) {
	a := initDiscoverRepo(t)
	b := initDiscoverRepo(t)
	roots := []projectroot.RootRef{
		{ID: "ra", Label: "alpha", Path: a, IsPrimary: true},
		{ID: "rb", Label: "beta", Path: b},
	}
	repos := git.DiscoverRepos(context.Background(), roots)
	if len(repos) != 2 {
		t.Fatalf("repos = %d want 2: %+v", len(repos), repos)
	}
	ids := map[string]git.RepoRef{}
	for _, r := range repos {
		if !r.Available {
			t.Fatalf("expected available: %+v", r)
		}
		ids[r.ID] = r
	}
	if len(ids) != 2 {
		t.Fatalf("want distinct ids, got %+v", ids)
	}
	byRoot := map[string]git.RepoRef{}
	for _, r := range repos {
		if len(r.RootIDs) != 1 {
			t.Fatalf("root_ids = %v want one", r.RootIDs)
		}
		byRoot[r.RootIDs[0]] = r
	}
	if byRoot["ra"].Label != "alpha" || byRoot["rb"].Label != "beta" {
		t.Fatalf("labels: %+v", byRoot)
	}
}

func TestDiscoverRepos_twoRootsOneCheckout(t *testing.T) {
	repo := initDiscoverRepo(t)
	web := filepath.Join(repo, "packages", "web")
	apiDir := filepath.Join(repo, "packages", "api")
	testutil.FailErr(t, "mkdir web", os.MkdirAll(web, 0o755))
	testutil.FailErr(t, "mkdir api", os.MkdirAll(apiDir, 0o755))

	roots := []projectroot.RootRef{
		{ID: "r-web", Label: "web", Path: web, IsPrimary: true},
		{ID: "r-api", Label: "api", Path: apiDir},
	}
	repos := git.DiscoverRepos(context.Background(), roots)
	if len(repos) != 1 {
		t.Fatalf("repos = %d want 1: %+v", len(repos), repos)
	}
	r := repos[0]
	if !r.Available || r.ID == "" {
		t.Fatalf("entry: %+v", r)
	}
	if got := strings.Join(r.RootIDs, ","); got != "r-web,r-api" {
		t.Fatalf("root_ids = %s want r-web,r-api (project-roots order)", got)
	}
	if r.Label != "web" {
		t.Fatalf("label = %q want primary-most member label", r.Label)
	}
	wantTop, err := filepath.EvalSymlinks(repo)
	testutil.FailErr(t, "eval repo", err)
	wantTop = filepath.Clean(wantTop)
	if r.Toplevel != wantTop {
		t.Fatalf("toplevel = %q want %q", r.Toplevel, wantTop)
	}
}

func TestDiscoverRepos_rootBelowToplevel(t *testing.T) {
	repo := initDiscoverRepo(t)
	web := filepath.Join(repo, "packages", "web")
	testutil.FailErr(t, "mkdir", os.MkdirAll(web, 0o755))
	roots := []projectroot.RootRef{{ID: "r1", Label: "web", Path: web, IsPrimary: true}}
	repos := git.DiscoverRepos(context.Background(), roots)
	if len(repos) != 1 || !repos[0].Available {
		t.Fatalf("repos: %+v", repos)
	}
	wantTop, err := filepath.EvalSymlinks(repo)
	testutil.FailErr(t, "eval", err)
	if repos[0].Toplevel != filepath.Clean(wantTop) {
		t.Fatalf("toplevel = %q want repo %q (not the root)", repos[0].Toplevel, wantTop)
	}
}

func TestDiscoverRepos_nonRepoRoot(t *testing.T) {
	dir := t.TempDir()
	roots := []projectroot.RootRef{{ID: "plain", Label: "docs", Path: dir, IsPrimary: true}}
	repos := git.DiscoverRepos(context.Background(), roots)
	if len(repos) != 1 {
		t.Fatalf("repos = %d want 1", len(repos))
	}
	r := repos[0]
	if r.Available || r.ID != "" || r.Toplevel != "" {
		t.Fatalf("non-repo entry: %+v", r)
	}
	if r.Label != "docs" || len(r.RootIDs) != 1 || r.RootIDs[0] != "plain" {
		t.Fatalf("non-repo shape: %+v", r)
	}
}

func TestDiscoverRepos_probeFailure(t *testing.T) {
	good := initDiscoverRepo(t)
	missing := filepath.Join(t.TempDir(), "gone")
	roots := []projectroot.RootRef{
		{ID: "ok", Label: "ok", Path: good, IsPrimary: true},
		{ID: "bad", Label: "bad", Path: missing},
	}
	repos := git.DiscoverRepos(context.Background(), roots)
	if len(repos) != 2 {
		t.Fatalf("repos = %d want 2: %+v", len(repos), repos)
	}
	var avail, failed int
	for _, r := range repos {
		if r.Available {
			avail++
			if r.RootIDs[0] != "ok" {
				t.Fatalf("available root: %+v", r)
			}
		} else {
			failed++
			if r.RootIDs[0] != "bad" || r.ID != "" {
				t.Fatalf("failed root: %+v", r)
			}
		}
	}
	if avail != 1 || failed != 1 {
		t.Fatalf("avail=%d failed=%d", avail, failed)
	}
}

func TestDiscoverRepos_symlinkSameID(t *testing.T) {
	repo := initDiscoverRepo(t)
	link := filepath.Join(t.TempDir(), "link")
	testutil.FailErr(t, "symlink", os.Symlink(repo, link))
	rootsDirect := []projectroot.RootRef{{ID: "d", Path: repo, IsPrimary: true}}
	rootsLink := []projectroot.RootRef{{ID: "l", Path: link, IsPrimary: true}}
	a := git.DiscoverRepos(context.Background(), rootsDirect)
	b := git.DiscoverRepos(context.Background(), rootsLink)
	if len(a) != 1 || len(b) != 1 || !a[0].Available || !b[0].Available {
		t.Fatalf("direct=%+v link=%+v", a, b)
	}
	if a[0].ID != b[0].ID {
		t.Fatalf("ids differ: %q vs %q", a[0].ID, b[0].ID)
	}
	if a[0].Toplevel != b[0].Toplevel {
		t.Fatalf("toplevels differ: %q vs %q", a[0].Toplevel, b[0].Toplevel)
	}
}

func TestDeriveRepoID_stableAndCanonical(t *testing.T) {
	dir := t.TempDir()
	id1 := git.DeriveRepoID(dir)
	id2 := git.DeriveRepoID(dir)
	if id1 == "" || id1 != id2 {
		t.Fatalf("stable id: %q vs %q", id1, id2)
	}
	if len(id1) != 17 || id1[0] != 'r' {
		t.Fatalf("id shape = %q want r + 16 hex", id1)
	}
	// Trailing separator must not mint a second id once canonicalized.
	withSep := dir + string(filepath.Separator)
	if git.DeriveRepoID(withSep) != id1 {
		t.Fatalf("trailing sep changed id: %q vs %q", git.DeriveRepoID(withSep), id1)
	}
	other := t.TempDir()
	if git.DeriveRepoID(other) == id1 {
		t.Fatalf("distinct paths must differ")
	}
}

func TestOrderRepos_activePrimaryLabelNonRepo(t *testing.T) {
	repos := []git.RepoRef{
		{ID: "r-z", Toplevel: "/z", Label: "zebra", RootIDs: []string{"z"}, Available: true},
		{ID: "r-a", Toplevel: "/a", Label: "alpha", RootIDs: []string{"a", "a2"}, Available: true},
		{ID: "", Toplevel: "", Label: "plain", RootIDs: []string{"p"}, Available: false},
		{ID: "r-p", Toplevel: "/p", Label: "primary", RootIDs: []string{"prim"}, Available: true},
	}
	ordered := git.OrderRepos(repos, "a2", "prim")
	want := []string{"r-a", "r-p", "r-z", ""}
	if len(ordered) != len(want) {
		t.Fatalf("len=%d: %+v", len(ordered), ordered)
	}
	for i, id := range want {
		if ordered[i].ID != id {
			t.Fatalf("pos %d id=%q want %q (%+v)", i, ordered[i].ID, id, ordered)
		}
	}
}

func TestOrderRepos_activeAndPrimarySameRepo(t *testing.T) {
	repos := []git.RepoRef{
		{ID: "r-other", Toplevel: "/o", Label: "other", RootIDs: []string{"o"}, Available: true},
		{ID: "r-main", Toplevel: "/m", Label: "main", RootIDs: []string{"prim", "active"}, Available: true},
	}
	ordered := git.OrderRepos(repos, "active", "prim")
	if ordered[0].ID != "r-main" {
		t.Fatalf("same-repo active+primary should be first: %+v", ordered)
	}
}

func TestDiscoverReposAgreesWithPathDiscovery(t *testing.T) {
	repo := initDiscoverRepo(t)
	web := filepath.Join(repo, "packages", "web")
	testutil.FailErr(t, "mkdir", os.MkdirAll(web, 0o755))

	found := git.DiscoverRepos(context.Background(), []projectroot.RootRef{
		{ID: "web", Path: web, IsPrimary: true},
	})
	if len(found) != 1 || !found[0].Available {
		t.Fatalf("DiscoverRepos from a package root = %+v", found)
	}
	walked, ok := gitrepo.Discover(web)
	if !ok {
		t.Fatal("gitrepo found no repository for the package root")
	}
	if found[0].Toplevel != walked.Root {
		t.Fatalf("git reported %q, path discovery reported %q", found[0].Toplevel, walked.Root)
	}
}

func TestReposGo_noShellOutEscape(t *testing.T) {
	srcPath := filepath.Join("repos.go")
	// Resolve relative to this test file's package directory via runtime.
	pkgDir, err := os.Getwd()
	testutil.FailErr(t, "wd", err)
	path := filepath.Join(pkgDir, srcPath)
	data, err := os.ReadFile(path)
	testutil.FailErr(t, "read repos.go", err)
	body := string(data)
	for _, needle := range []string{`exec.Command`, `LookPath`, `".git"`, `'.git'`} {
		if strings.Contains(body, needle) {
			t.Fatalf("repos.go must not contain %s", needle)
		}
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, data, 0)
	testutil.FailErr(t, "parse", err)
	ast.Inspect(f, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		id, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		if id.Name == "exec" && (sel.Sel.Name == "Command" || sel.Sel.Name == "CommandContext" || sel.Sel.Name == "LookPath") {
			t.Fatalf("repos.go must not call exec.%s", sel.Sel.Name)
		}
		return true
	})
}

func TestLabelFor_primaryMostWins(t *testing.T) {
	members := []projectroot.RootRef{
		{ID: "a", Label: "api", Path: "/repo/api"},
		{ID: "w", Label: "web", Path: "/repo/web", IsPrimary: true},
	}
	if got := git.LabelFor(members, "/repo"); got != "web" {
		t.Fatalf("label = %q want web", got)
	}
	if got := git.LabelFor([]projectroot.RootRef{{ID: "x", Path: "/repo/pkg"}}, "/repo"); got != "repo" {
		t.Fatalf("fallback base = %q want repo", got)
	}
}
