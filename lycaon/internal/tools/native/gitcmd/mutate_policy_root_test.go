package gitcmd

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/agentpolicy"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
)

func TestWithRepoRootKeepsAttachedPolicyScope(t *testing.T) {
	primary := projectroot.RootRef{ID: "primary", Path: t.TempDir(), IsPrimary: true}
	linked := projectroot.RootRef{ID: "linked", Path: t.TempDir()}
	roots := []projectroot.RootRef{primary, linked}
	tc := tools.ToolContext{Source: tools.InvocationSource{Roots: roots, ActiveRootID: primary.ID}}

	scoped := withRepoRoot(tc, linked.Path)
	if scoped.Source.ActiveRootID != linked.ID || !slices.Equal(scoped.Source.Roots, roots) {
		t.Fatalf("scoped Git context = %+v, want original roots and active ID %q", scoped.Source, linked.ID)
	}
	want := agentpolicy.Target{RootID: linked.ID, Path: filepath.Join(linked.Path, "shared.txt"), Mutation: true}
	var checked []agentpolicy.Target
	ctx := tools.WithAgentPolicy(t.Context(), func(_ context.Context, targets []agentpolicy.Target) error {
		checked = append(checked, targets...)
		return nil
	})
	testutil.FailErr(t, "review Git path", scoped.CheckScopePolicy(ctx, scoped.Source.ActiveRootID, "shared.txt", true))
	if !slices.Equal(checked, []agentpolicy.Target{want}) {
		t.Fatalf("Git policy checks = %+v, want %+v", checked, want)
	}
}
