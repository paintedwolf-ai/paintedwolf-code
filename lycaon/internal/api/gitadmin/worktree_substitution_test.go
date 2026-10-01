package gitadmin

import (
	"testing"

	"github.com/lycaon/lycaon/internal/git"
)

func TestSubstituteWorktreeRepoReaddressesTheBoundRepository(t *testing.T) {
	base := []git.RepoRef{
		{ID: "repo-a", Toplevel: "/checkouts/a", RootIDs: []string{"root-a"}, Available: true},
		{ID: "repo-b", Toplevel: "/checkouts/b", RootIDs: []string{"root-b"}, Available: true},
	}
	got := substituteWorktreeRepo(base, "repo-a", "/worktrees/a-feature")
	if got[0].Toplevel != "/worktrees/a-feature" || got[0].ID != "repo-a" || got[1].Toplevel != "/checkouts/b" {
		t.Fatalf("substitution = %+v", got)
	}
	if base[0].Toplevel != "/checkouts/a" {
		t.Fatal("the cached set was mutated")
	}
	got[0].RootIDs[0] = "changed"
	if base[0].RootIDs[0] != "root-a" {
		t.Fatal("the cached set shares root id storage with the substitution")
	}
}

func TestSubstituteWorktreeRepoAddsAnUnknownBinding(t *testing.T) {
	got := substituteWorktreeRepo(nil, "repo-new", "/worktrees/new")
	if len(got) != 1 || got[0].ID != "repo-new" || got[0].Toplevel != "/worktrees/new" || !got[0].Available {
		t.Fatalf("unknown binding = %+v", got)
	}
	if got := substituteWorktreeRepo(nil, "", ""); len(got) != 0 {
		t.Fatalf("empty binding fabricated a repo: %+v", got)
	}
}
