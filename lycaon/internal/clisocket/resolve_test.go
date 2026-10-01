package clisocket

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func proj(id, name string, opened time.Time, roots ...project.Root) project.Project {
	for i := range roots {
		if i == 0 {
			roots[i].IsPrimary = true
		}
	}
	return project.Project{ID: id, Name: name, Roots: roots, LastOpenedAt: opened}
}

func root(path string) project.Root {
	return project.Root{ID: "r-" + filepath.Base(path), Path: path}
}

func TestResolvePathExactRootOpensThatProject(t *testing.T) {
	dir := t.TempDir()
	projects := []project.Project{proj("p1", "app", time.Now(), root(dir))}

	ev := ResolvePath(projects, dir)

	if ev.Action != api.CLIOpenActionOpen {
		t.Fatalf("action = %q, want open", ev.Action)
	}
	if ev.ProjectID == nil || *ev.ProjectID != "p1" {
		t.Fatalf("project = %v, want p1", ev.ProjectID)
	}
}

func TestResolvePathSubdirectoryOpensEnclosingProject(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "src", "api")
	testutil.FailErr(t, "make subdir", os.MkdirAll(sub, 0o700))
	projects := []project.Project{proj("p1", "app", time.Now(), root(dir))}

	ev := ResolvePath(projects, sub)

	if ev.Action != api.CLIOpenActionOpen {
		t.Fatalf("action = %q, want open", ev.Action)
	}
	if ev.ProjectID == nil || *ev.ProjectID != "p1" {
		t.Fatalf("project = %v, want p1", ev.ProjectID)
	}
}

func TestResolvePathExactBeatsAncestor(t *testing.T) {
	parent := t.TempDir()
	child := filepath.Join(parent, "child")
	testutil.FailErr(t, "make child", os.MkdirAll(child, 0o700))
	now := time.Now()
	projects := []project.Project{
		proj("outer", "outer", now, root(parent)),
		proj("inner", "inner", now.Add(-time.Hour), root(child)),
	}

	ev := ResolvePath(projects, child)

	if ev.ProjectID == nil || *ev.ProjectID != "inner" {
		t.Fatalf("project = %v, want inner", ev.ProjectID)
	}
}

func TestResolvePathDeepestAncestorWins(t *testing.T) {
	outer := t.TempDir()
	inner := filepath.Join(outer, "packages", "web")
	leaf := filepath.Join(inner, "src")
	testutil.FailErr(t, "make leaf", os.MkdirAll(leaf, 0o700))
	now := time.Now()
	projects := []project.Project{
		proj("outer", "outer", now, root(outer)),
		proj("inner", "inner", now.Add(-time.Hour), root(inner)),
	}

	ev := ResolvePath(projects, leaf)

	if ev.ProjectID == nil || *ev.ProjectID != "inner" {
		t.Fatalf("project = %v, want inner (deepest root)", ev.ProjectID)
	}
}

func TestResolvePathUnknownFolderProposesCreate(t *testing.T) {
	dir := t.TempDir()
	other := t.TempDir()
	projects := []project.Project{proj("p1", "app", time.Now(), root(other))}

	ev := ResolvePath(projects, dir)

	if ev.Action != api.CLIOpenActionCreate {
		t.Fatalf("action = %q, want create", ev.Action)
	}
	if ev.ProjectID != nil {
		t.Fatalf("project = %v, want nil on create", *ev.ProjectID)
	}
}

func TestResolvePathMatchesThroughSymlink(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	testutil.FailErr(t, "symlink", os.Symlink(real, link))
	projects := []project.Project{proj("p1", "app", time.Now(), root(real))}

	ev := ResolvePath(projects, link)

	if ev.Action != api.CLIOpenActionOpen {
		t.Fatalf("action = %q, want open through symlink", ev.Action)
	}
	if ev.ProjectID == nil || *ev.ProjectID != "p1" {
		t.Fatalf("project = %v, want p1", ev.ProjectID)
	}
}

func TestResolvePathSiblingSharingPrefixIsNotContained(t *testing.T) {
	base := t.TempDir()
	bc := filepath.Join(base, "bc")
	bcd := filepath.Join(base, "bcd")
	testutil.FailErr(t, "make bc", os.MkdirAll(bc, 0o700))
	testutil.FailErr(t, "make bcd", os.MkdirAll(bcd, 0o700))
	projects := []project.Project{proj("p1", "bc", time.Now(), root(bc))}

	ev := ResolvePath(projects, bcd)

	if ev.Action != api.CLIOpenActionCreate {
		t.Fatalf("action = %q, want create — bcd is not inside bc", ev.Action)
	}
}

func TestResolvePathDuplicateRootsPickMostRecentlyOpened(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	projects := []project.Project{
		proj("old", "app", now.Add(-time.Hour), root(dir)),
		proj("new", "app", now, root(dir)),
	}

	ev := ResolvePath(projects, dir)

	if ev.ProjectID == nil || *ev.ProjectID != "new" {
		t.Fatalf("project = %v, want new", ev.ProjectID)
	}
}

func TestResolvePathMatchesSecondaryRoot(t *testing.T) {
	primary := t.TempDir()
	secondary := t.TempDir()
	projects := []project.Project{
		proj("p1", "app", time.Now(), root(primary), root(secondary)),
	}

	ev := ResolvePath(projects, secondary)

	if ev.ProjectID == nil || *ev.ProjectID != "p1" {
		t.Fatalf("project = %v, want p1 via secondary root", ev.ProjectID)
	}
}

func TestDisplayNameUsesPrimaryRootBasename(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "my-repo")
	testutil.FailErr(t, "make dir", os.MkdirAll(dir, 0o700))
	unnamed := proj("p1", "", time.Now(), root(dir))

	if got := DisplayName(unnamed); got != "my-repo" {
		t.Fatalf("DisplayName = %q, want my-repo", got)
	}
	unnamed.Roots[0].IsPrimary = false
	if got := DisplayName(unnamed); got != "" {
		t.Fatalf("name without primary = %q, want empty", got)
	}
}

func TestResolveNameReturnsEveryCollision(t *testing.T) {
	now := time.Now()
	projects := []project.Project{
		proj("a", "app", now.Add(-time.Hour), root(t.TempDir())),
		proj("b", "app", now, root(t.TempDir())),
		proj("c", "other", now, root(t.TempDir())),
	}

	matches := ResolveName(projects, "app")

	if len(matches) != 2 {
		t.Fatalf("matches = %d, want 2", len(matches))
	}
	if matches[0].ID != "b" {
		t.Fatalf("first match = %q, want b (most recently opened)", matches[0].ID)
	}
}

func TestResolveNameIsCaseInsensitive(t *testing.T) {
	projects := []project.Project{proj("a", "MyApp", time.Now(), root(t.TempDir()))}

	if matches := ResolveName(projects, "myapp"); len(matches) != 1 {
		t.Fatalf("matches = %d, want 1", len(matches))
	}
}

func TestLooksLikePath(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		arg  string
		want bool
	}{
		{".", true},
		{"..", true},
		{"~/dev/app", true},
		{"/abs/path", true},
		{"rel/path", true},
		{"my-project", false},
		{dir, true},
	}
	for _, tc := range cases {
		if got := LooksLikePath(tc.arg); got != tc.want {
			t.Errorf("LooksLikePath(%q) = %v, want %v", tc.arg, got, tc.want)
		}
	}
}
