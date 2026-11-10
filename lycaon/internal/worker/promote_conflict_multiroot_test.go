package worker

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestQualifyBranchRelUsesStableRootDirectory(t *testing.T) {
	roots := []projectroot.RootRef{
		{ID: "primary-id", Label: "app", Path: "/project/app", IsPrimary: true},
		{ID: "secondary-id", Label: "docs", Path: "/project/docs"},
	}
	branchDir, err := projectroot.BranchDirForID(roots[1].ID)
	testutil.FailErr(t, "secondary branch directory", err)

	var got string
	qualifyBranchRel(PromoteRoots{Roots: roots}, roots[0], branchDir+"/notes..draft.md", func(path string) {
		got = path
	})
	if got != "@docs/notes..draft.md" {
		t.Fatalf("qualified path = %q", got)
	}
}

func TestExpandOverlayPromoteFilesMultiRootUsesStructuredMappingOnCollision(t *testing.T) {
	branchRoot := t.TempDir()
	primaryRoot := t.TempDir()
	secondaryRoot := t.TempDir()
	roots := []projectroot.RootRef{
		{ID: "primary-id", Label: "app", Path: primaryRoot, IsPrimary: true},
		{ID: "secondary-id", Label: "docs", Path: secondaryRoot},
	}
	primaryDir, err := projectroot.BranchDirForID(roots[0].ID)
	testutil.FailErr(t, "primary branch dir", err)
	secondaryDir, err := projectroot.BranchDirForID(roots[1].ID)
	testutil.FailErr(t, "secondary branch dir", err)

	// Decoy files/directories directly under BranchRoot that collide with model-path spellings
	// ("conflict.txt" and "@docs/guide.md") or branch-dir spellings under primaryDir.
	testutil.FailErr(t, "mkdir decoy dir at branch root", os.MkdirAll(filepath.Join(branchRoot, "conflict.txt"), 0o700))
	testutil.FailErr(t, "write decoy child under branch root conflict.txt", os.WriteFile(filepath.Join(branchRoot, "conflict.txt", "decoy.txt"), []byte("decoy"), 0o600))
	testutil.FailErr(t, "mkdir decoy @docs at branch root", os.MkdirAll(filepath.Join(branchRoot, "@docs"), 0o700))
	testutil.FailErr(t, "write decoy @docs/guide.md at branch root", os.WriteFile(filepath.Join(branchRoot, "@docs", "guide.md"), []byte("decoy"), 0o600))

	// Real branch files mapped via projectroot.BranchDirForID.
	realPrimaryFile := filepath.Join(branchRoot, primaryDir, "conflict.txt")
	testutil.FailErr(t, "mkdir primary branch dir", os.MkdirAll(filepath.Dir(realPrimaryFile), 0o700))
	testutil.FailErr(t, "write primary conflict.txt", os.WriteFile(realPrimaryFile, []byte("real-primary"), 0o600))

	realSecondaryFile := filepath.Join(branchRoot, secondaryDir, "sub", "guide.md")
	testutil.FailErr(t, "mkdir secondary branch dir", os.MkdirAll(filepath.Dir(realSecondaryFile), 0o700))
	testutil.FailErr(t, "write secondary guide.md", os.WriteFile(realSecondaryFile, []byte("real-secondary"), 0o600))

	promote := PromoteRoots{BranchRoot: branchRoot, Roots: roots}
	got, err := expandOverlayPromoteFiles(promote, []string{"conflict.txt", "@docs/sub", secondaryDir + "/sub/guide.md"})
	testutil.FailErr(t, "expandOverlayPromoteFiles", err)
	want := []string{"@docs/sub/guide.md", "conflict.txt"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("expandOverlayPromoteFiles = %v, want %v", got, want)
	}
}
