package tools

import (
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
)

// A read-only view is read back, never written: processes write only the
// session's scratch, and the view's sources stay denied as for any branch.
func TestReadOnlyConfinesProcessesToScratch(t *testing.T) {
	tctx := ToolContext{
		Identity: InvocationIdentity{WorkerJobID: "review-job"},
		Host:     InvocationHost{SessionScratchDir: "/scratch/session"},
		Source: InvocationSource{
			WorkerBranchRoot:  "/views/review",
			ReadOnly:          true,
			WorkerSourceRoots: []string{"/src/project"},
			Roots:             []projectroot.RootRef{{ID: "root", Path: "/src/project", IsPrimary: true}},
		},
	}
	if got := ConfineRootsForAction(tctx); !slices.Equal(got, []string{"/scratch/session"}) {
		t.Fatalf("write jail = %v", got)
	}
	if got := ConfineReadRootsForAction(tctx); !slices.Contains(got, "/views/review") {
		t.Fatalf("read allow-backs = %v", got)
	}
	in := ActionConfineInputsForContext(tctx, nil)
	if !slices.Contains(in.ReadDenyPaths, "/src/project") || slices.Contains(in.Roots, "/views/review") {
		t.Fatalf("confine inputs = %+v", in)
	}
}
