package project

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
)

func TestSubstituteWorktreeRoots_toplevelRoot(t *testing.T) {
	top := filepath.Join(t.TempDir(), "repo")
	wt := filepath.Join(t.TempDir(), "wt")
	roots := []projectroot.RootRef{
		{ID: "r1", Label: "main", Path: top, IsPrimary: true},
	}
	got := SubstituteWorktreeRoots(roots, Binding{Toplevel: top, WorktreePath: wt})
	if len(got) != 1 {
		t.Fatalf("len = %d", len(got))
	}
	if got[0].Path != wt {
		t.Fatalf("path = %q want %q", got[0].Path, wt)
	}
	if got[0].ID != "r1" || got[0].Label != "main" || !got[0].IsPrimary {
		t.Fatalf("identity mutated: %+v", got[0])
	}
}

func TestSubstituteWorktreeRoots_rootBelowToplevel(t *testing.T) {
	top := filepath.Join(t.TempDir(), "repo")
	web := filepath.Join(top, "packages", "web")
	wt := filepath.Join(t.TempDir(), "wt")
	roots := []projectroot.RootRef{
		{ID: "web", Label: "web", Path: web, IsPrimary: true},
	}
	got := SubstituteWorktreeRoots(roots, Binding{Toplevel: top, WorktreePath: wt})
	want := filepath.Join(wt, "packages", "web")
	if got[0].Path != want {
		t.Fatalf("path = %q want %q", got[0].Path, want)
	}
	if got[0].ID != "web" || got[0].Label != "web" || !got[0].IsPrimary {
		t.Fatalf("identity mutated: %+v", got[0])
	}
}

func TestSubstituteWorktreeRoots_foreignRoot(t *testing.T) {
	top := filepath.Join(t.TempDir(), "repo-a")
	other := filepath.Join(t.TempDir(), "repo-b")
	wt := filepath.Join(t.TempDir(), "wt")
	roots := []projectroot.RootRef{
		{ID: "a", Label: "a", Path: top, IsPrimary: true},
		{ID: "b", Label: "b", Path: other},
	}
	got := SubstituteWorktreeRoots(roots, Binding{Toplevel: top, WorktreePath: wt})
	if got[0].Path != wt {
		t.Fatalf("bound root = %q want %q", got[0].Path, wt)
	}
	if got[1] != roots[1] {
		t.Fatalf("foreign root changed: %+v want %+v", got[1], roots[1])
	}
}

func TestSubstituteWorktreeRoots_inputNotMutated(t *testing.T) {
	top := filepath.Join(t.TempDir(), "repo")
	wt := filepath.Join(t.TempDir(), "wt")
	roots := []projectroot.RootRef{
		{ID: "r1", Label: "main", Path: top, IsPrimary: true},
	}
	origPath := roots[0].Path
	_ = SubstituteWorktreeRoots(roots, Binding{Toplevel: top, WorktreePath: wt})
	if roots[0].Path != origPath {
		t.Fatalf("input mutated: %q", roots[0].Path)
	}
}

func TestSubstituteWorktreeRoots_emptyBinding(t *testing.T) {
	roots := []projectroot.RootRef{
		{ID: "r1", Path: "/repo", IsPrimary: true},
	}
	got := SubstituteWorktreeRoots(roots, Binding{})
	if len(got) != 1 || got[0].Path != "/repo" {
		t.Fatalf("empty binding must leave roots unchanged: %+v", got)
	}
	got[0].Path = "mutated"
	if roots[0].Path != "mutated" {
		t.Fatal("empty binding must return the input slice, not a copy")
	}
}
