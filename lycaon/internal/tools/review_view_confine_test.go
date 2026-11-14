package tools

import (
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
)

// A reviewer's processes are jailed in its own view and read it, not the
// sources it was prepared from; file tools find no branch to write.
func TestReviewViewConfinesAReviewer(t *testing.T) {
	tctx := ToolContext{
		Identity: InvocationIdentity{WorkerJobID: "review-job"},
		Source: InvocationSource{
			ReviewViewRoot:    "/views/review",
			WorkerSourceRoots: []string{"/src/project"},
			Roots:             []projectroot.RootRef{{ID: "root", Path: "/views/review", IsPrimary: true}},
		},
	}
	if got := ConfineRootsForAction(tctx); !slices.Equal(got, []string{"/views/review"}) {
		t.Fatalf("write jail = %v", got)
	}
	if got := HostWriteRoot(tctx); got != "/views/review" {
		t.Fatalf("process root = %q", got)
	}
	if got := ConfineReadRootsForAction(tctx); !slices.Contains(got, "/views/review") {
		t.Fatalf("read allow-backs = %v", got)
	}
	in := ActionConfineInputsForContext(tctx, nil)
	if !slices.Contains(in.ReadDenyPaths, "/src/project") || slices.Contains(in.Roots, "/src/project") {
		t.Fatalf("confine inputs = %+v", in)
	}
	if tctx.Source.WorkerBranchRoot != "" {
		t.Fatal("a reviewer has no branch root")
	}
}
