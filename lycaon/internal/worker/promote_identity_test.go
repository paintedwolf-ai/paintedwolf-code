package worker

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestExpandOverlayPromoteFilesMultiRootPopulatesAllowedSetFromBaselineSpellings(t *testing.T) {
	base := t.TempDir()
	primary := filepath.Join(base, "a")
	secondary := filepath.Join(base, "b")
	branchRoot := filepath.Join(base, "branch")
	primaryDir, err := projectroot.BranchDirForID("primary-id")
	testutil.FailErr(t, "primary branch dir", err)
	secondaryDir, err := projectroot.BranchDirForID("secondary-id")
	testutil.FailErr(t, "secondary branch dir", err)
	for _, dir := range []string{
		primary, secondary,
		filepath.Join(branchRoot, primaryDir),
		filepath.Join(branchRoot, secondaryDir, "guides"),
	} {
		testutil.FailErr(t, "mkdir", os.MkdirAll(dir, 0o755))
	}
	testutil.FailErr(t, "write primary notes", os.WriteFile(filepath.Join(branchRoot, primaryDir, "notes.md"), []byte("x"), 0o644))
	testutil.FailErr(t, "write docs readme", os.WriteFile(filepath.Join(branchRoot, secondaryDir, "readme.md"), []byte("x"), 0o644))
	testutil.FailErr(t, "write docs plan", os.WriteFile(filepath.Join(branchRoot, secondaryDir, "guides", "plan.md"), []byte("x"), 0o644))

	roots := []projectroot.RootRef{
		{ID: "primary-id", Label: "app", Path: primary, IsPrimary: true},
		{ID: "secondary-id", Label: "docs", Path: secondary},
	}
	// Active root differs from primary: the allowed set must still be spelled
	// in baseline grammar (primary files bare, others "@label/rel").
	promote := PromoteRoots{Roots: roots, BranchRoot: branchRoot, ActiveRootID: "secondary-id"}
	got, err := expandOverlayPromoteFilesMultiRoot(promote, []string{
		"notes.md",
		"@docs/readme.md",
		"@docs/guides",
		primaryDir + "/notes.md",
	})
	testutil.FailErr(t, "expand", err)
	want := []string{"@docs/guides/plan.md", "@docs/readme.md", "notes.md"}
	if !slices.Equal(got, want) {
		t.Fatalf("allowed set = %q want %q", got, want)
	}
}

func TestReadPairBytesAtResolvesStructuredIdentity(t *testing.T) {
	base := t.TempDir()
	primary := filepath.Join(base, "a")
	secondary := filepath.Join(base, "b")
	branchRoot := filepath.Join(base, "branch")
	secondaryDir, err := projectroot.BranchDirForID("secondary-id")
	testutil.FailErr(t, "secondary branch dir", err)
	for _, dir := range []string{primary, secondary, filepath.Join(branchRoot, secondaryDir)} {
		testutil.FailErr(t, "mkdir", os.MkdirAll(dir, 0o755))
	}
	testutil.FailErr(t, "write primary", os.WriteFile(filepath.Join(primary, "notes.md"), []byte("primary\n"), 0o644))
	testutil.FailErr(t, "write docs", os.WriteFile(filepath.Join(secondary, "notes.md"), []byte("docs primary\n"), 0o644))
	testutil.FailErr(t, "write overlay", os.WriteFile(filepath.Join(branchRoot, secondaryDir, "notes.md"), []byte("docs branch\n"), 0o644))

	roots := []projectroot.RootRef{
		{ID: "primary-id", Label: "app", Path: primary, IsPrimary: true},
		{ID: "secondary-id", Label: "docs", Path: secondary},
	}
	task := &api.WorkerTask{WorkspacePath: primary, WorkspaceRoot: branchRoot, WorkspaceRootID: "secondary-id"}
	promote := PromoteRoots{Roots: roots, BranchRoot: branchRoot, ActiveRootID: "secondary-id"}

	primaryBytes, branchBytes, present, err := promote.ReadPairBytesAt(task, "secondary-id", "notes.md")
	testutil.FailErr(t, "ReadPairBytesAt docs", err)
	if !present || string(primaryBytes) != "docs primary\n" || string(branchBytes) != "docs branch\n" {
		t.Fatalf("docs pair = (%q, %q, %v)", primaryBytes, branchBytes, present)
	}
	primaryBytes, branchBytes, present, err = promote.ReadPairBytesAt(task, "primary-id", "notes.md")
	testutil.FailErr(t, "ReadPairBytesAt primary", err)
	if present || string(primaryBytes) != "primary\n" || len(branchBytes) != 0 {
		t.Fatalf("primary pair = (%q, %q, %v)", primaryBytes, branchBytes, present)
	}
}

func TestLocateIdentityMapsBaselineSpellings(t *testing.T) {
	base := t.TempDir()
	primary := filepath.Join(base, "a")
	secondary := filepath.Join(base, "b")
	branchRoot := filepath.Join(base, "branch")
	primaryDir, err := projectroot.BranchDirForID("primary-id")
	testutil.FailErr(t, "primary branch dir", err)
	secondaryDir, err := projectroot.BranchDirForID("secondary-id")
	testutil.FailErr(t, "secondary branch dir", err)
	testutil.FailErr(t, "mkdir branch", os.MkdirAll(filepath.Join(branchRoot, secondaryDir), 0o755))

	roots := []projectroot.RootRef{
		{ID: "primary-id", Label: "app", Path: primary, IsPrimary: true},
		{ID: "secondary-id", Label: "docs", Path: secondary},
	}
	// Active root is secondary: a bare baseline spelling must still address
	// the primary root, not the active one.
	promote := PromoteRoots{Roots: roots, BranchRoot: branchRoot, ActiveRootID: "secondary-id"}
	task := &api.WorkerTask{WorkspacePath: primary, WorkspaceRoot: branchRoot, WorkspaceRootID: "secondary-id"}

	id, err := promote.locateIdentity(task, "notes.md")
	testutil.FailErr(t, "locateIdentity", err)
	if id.RootID != "primary-id" || id.ScopeRel != "notes.md" {
		t.Fatalf("identity = (%q, %q)", id.RootID, id.ScopeRel)
	}
	if id.Primary != filepath.Join(primary, "notes.md") {
		t.Fatalf("primary abs = %q", id.Primary)
	}
	if id.Branch != filepath.Join(branchRoot, primaryDir, "notes.md") {
		t.Fatalf("branch abs = %q", id.Branch)
	}

	id, err = promote.locateIdentity(task, "@DOCS/notes.md")
	testutil.FailErr(t, "locateIdentity labeled", err)
	if id.RootID != "secondary-id" {
		t.Fatalf("labeled identity root = %q", id.RootID)
	}
	if id.Primary != filepath.Join(secondary, "notes.md") {
		t.Fatalf("labeled primary abs = %q", id.Primary)
	}
}
