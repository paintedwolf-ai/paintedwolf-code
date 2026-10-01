package worker

import (
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
