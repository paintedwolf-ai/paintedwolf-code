package tools

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/agentpolicy"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestPolicyAdmissionUsesTheMostSpecificPhysicalRoot(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "nested")
	tc := ToolContext{Source: InvocationSource{Roots: []projectroot.RootRef{
		{ID: "outer", Path: root, Label: "outer", IsPrimary: true},
		{ID: "inner", Path: nested, Label: "inner"},
	}}}
	var addressed []string
	ctx := WithAgentPolicy(t.Context(), func(_ context.Context, targets []agentpolicy.Target) error {
		for _, target := range targets {
			if !target.Mutation {
				t.Fatal("mutation was classified as a read")
			}
			addressed = append(addressed, target.RootID+":"+target.Path)
		}
		return nil
	})
	file := filepath.Join(nested, "file.go")
	testutil.FailErr(t, "physical scope", tc.CheckPathPolicy(ctx, file, true))
	testutil.FailErr(t, "qualified scope", tc.CheckScopePolicy(ctx, "outer", "@inner/file.go", true))
	testutil.FailErr(t, "unattached scratch", tc.CheckPathPolicy(ctx, filepath.Join(t.TempDir(), "scratch"), true))
	if len(addressed) != 2 || addressed[0] != "inner:"+file || addressed[1] != addressed[0] {
		t.Fatalf("policy addresses = %v", addressed)
	}
}

func TestHeldPolicyContextDoesNotBorrowALaterRequest(t *testing.T) {
	tc := ToolContext{Source: InvocationSource{Roots: []projectroot.RootRef{{ID: "root", Path: t.TempDir(), IsPrimary: true}}}}
	oldFailure := errors.New("old request omitted policy")
	old := WithAgentPolicy(t.Context(), func(context.Context, []agentpolicy.Target) error { return oldFailure })
	held := context.WithoutCancel(old)
	fresh := WithAgentPolicy(old, func(context.Context, []agentpolicy.Target) error { return nil })
	target := filepath.Join(tc.Source.Roots[0].Path, "file.go")
	testutil.FailErr(t, "fresh request", tc.CheckPathPolicy(fresh, target, true))
	if err := tc.CheckPathPolicy(held, target, true); !errors.Is(err, oldFailure) {
		t.Fatalf("held request policy = %v", err)
	}
}

func TestPolicyReviewCoversBothTransferEndpointsBeforeApproval(t *testing.T) {
	root := t.TempDir()
	tc := ToolContext{Source: InvocationSource{Roots: []projectroot.RootRef{{ID: "root", Path: root, IsPrimary: true}}}}
	denied := errors.New("source policy omitted")
	from, to := filepath.Join(root, "from"), filepath.Join(root, "to")
	var paths []string
	ctx := WithAgentPolicy(t.Context(), func(_ context.Context, targets []agentpolicy.Target) error {
		for _, target := range targets {
			paths = append(paths, target.Path)
			if target.Path == from {
				return denied
			}
		}
		return nil
	})
	approved := false
	tc.Files.FileChangeReview = func(context.Context, []FileChange) error { approved = true; return nil }
	err := tc.ReviewFileChanges(ctx, FileChange{Path: to, FromPath: from})
	if !errors.Is(err, denied) || approved || len(paths) != 2 {
		t.Fatalf("transfer admission: error=%v approved=%v paths=%v", err, approved, paths)
	}
}
