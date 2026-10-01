package project

import "testing"

func projectWithRoots(id string, paths ...string) Project {
	p := Project{ID: id}
	for i, path := range paths {
		p.Roots = append(p.Roots, Root{Path: path, IsPrimary: i == 0})
	}
	return p
}

func TestFindByRootPathMatchesAnyRoot(t *testing.T) {
	list := []Project{
		projectWithRoots("a", "/repos/alpha"),
		projectWithRoots("b", "/repos/beta", "/repos/beta-docs"),
	}
	for _, tc := range []struct{ path, want string }{
		{"/repos/alpha", "a"},
		{"/repos/beta", "b"},
		// A secondary root counts: the folder is covered either way, and opening it
		// must not mint a project that duplicates one of b's trees.
		{"/repos/beta-docs", "b"},
	} {
		got := FindByRootPath(list, tc.path)
		if got == nil {
			t.Fatalf("FindByRootPath(%q) = nil, want project %s", tc.path, tc.want)
		}
		if got.ID != tc.want {
			t.Fatalf("FindByRootPath(%q) = %s, want %s", tc.path, got.ID, tc.want)
		}
	}
}

func TestFindByRootPathUnknownFolder(t *testing.T) {
	list := []Project{projectWithRoots("a", "/repos/alpha")}
	for _, path := range []string{"/repos/gamma", "/repos/alpha/nested", "", "   "} {
		if got := FindByRootPath(list, path); got != nil {
			t.Fatalf("FindByRootPath(%q) = %s, want nil", path, got.ID)
		}
	}
}

// A tree held by two projects resolves to the first in registry order, which is
// the most recently opened one — the project the human was last in.
func TestFindByRootPathPrefersRegistryOrder(t *testing.T) {
	list := []Project{
		projectWithRoots("recent", "/repos/alpha"),
		projectWithRoots("stale", "/repos/alpha"),
	}
	got := FindByRootPath(list, "/repos/alpha")
	if got == nil || got.ID != "recent" {
		t.Fatalf("FindByRootPath = %v, want recent", got)
	}
}
