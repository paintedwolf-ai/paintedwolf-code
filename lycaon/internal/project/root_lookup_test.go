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

func TestFindAllByRootPathOrdersSingleRootBeforeMultiRoot(t *testing.T) {
	list := []Project{
		projectWithRoots("multi-recent", "/repos/main", "/repos/shared"),
		projectWithRoots("single", "/repos/shared"),
		projectWithRoots("multi-older", "/repos/shared", "/repos/docs"),
	}
	matches := FindAllByRootPath(list, "/repos/shared")
	if len(matches) != 3 {
		t.Fatalf("len(matches) = %d, want 3", len(matches))
	}
	wantIDs := []string{"single", "multi-recent", "multi-older"}
	for i, want := range wantIDs {
		if matches[i].ID != want {
			t.Fatalf("matches[%d].ID = %q, want %q", i, matches[i].ID, want)
		}
	}
	if !IsSingleRootForPath(&matches[0], "/repos/shared") {
		t.Fatal("expected single-root project to report IsSingleRootForPath=true")
	}
	if IsSingleRootForPath(&matches[1], "/repos/shared") {
		t.Fatal("expected multi-root project to report IsSingleRootForPath=false")
	}
	if got := FindByRootPath(list, "/repos/shared"); got == nil || got.ID != "single" {
		t.Fatalf("FindByRootPath = %v, want single", got)
	}
}

func TestToFolderDetectProjectIncludesNameAndRootMetadata(t *testing.T) {
	named := projectWithRoots("p1", "/repos/alpha", "/repos/beta")
	named.Name = "Workspace"
	dto := ToFolderDetectProject(&named, "/repos/beta")
	if dto.ID != "p1" || dto.Name != "Workspace" || dto.IsSingleRoot || len(dto.Roots) != 2 {
		t.Fatalf("ToFolderDetectProject(multi) = %+v", dto)
	}

	unnamed := projectWithRoots("p2", "/repos/solo")
	singleDTO := ToFolderDetectProject(&unnamed, "/repos/solo")
	if singleDTO.ID != "p2" || singleDTO.Name != "solo" || !singleDTO.IsSingleRoot || len(singleDTO.Roots) != 1 {
		t.Fatalf("ToFolderDetectProject(single) = %+v", singleDTO)
	}
	if empty := ToFolderDetectProject(nil, "/repos/solo"); empty.ID != "" {
		t.Fatalf("ToFolderDetectProject(nil) = %+v, want empty", empty)
	}
}
