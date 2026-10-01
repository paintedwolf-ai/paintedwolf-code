package tools

import (
	"context"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

func TestNativeContextReceivesCurrentConfinementWriteGrants(t *testing.T) {
	project, granted := t.TempDir(), t.TempDir()
	registry := NewDefaultRegistry()
	var received ToolContext
	const tool = "mcp_fixture_context"
	testutil.FailErr(t, "register context probe", registry.RegisterDefinition(Definition{
		Contract: toolcontract.External("fixture"),
		Meta:     ToolMeta{Name: tool, Description: "Inspect context", ArgsSchema: map[string]any{"type": "object"}},
		Handler: func(_ context.Context, _ map[string]any, tc ToolContext) (string, error) {
			received = tc
			return "ok", nil
		},
	}))
	policy := &capturePolicy{}
	executor := NewDefaultToolExecutor(policy, registry, "implement")
	active := true
	executor.SetSessionWriteRootOverlay(func(_ context.Context, session, _ string) []string {
		if session == "approved" && active {
			return []string{granted}
		}
		return nil
	})
	for _, session := range []string{"approved", "other", "approved"} {
		tc := ToolContext{ProjectID: "project", SessionID: session,
			Roots: []projectroot.RootRef{{ID: "root", Path: project, IsPrimary: true}}, ActiveRootID: "root"}
		_, err := executor.Invoke(t.Context(), tool, map[string]any{}, tc)
		testutil.FailErr(t, "invoke context probe", err)
		if !slices.Equal(received.GrantedWriteRoots, policy.eval.ConfineRequest.GrantedWriteRoots) {
			t.Fatal("native context diverged from the approved process boundary")
		}
		if got := len(received.GrantedWriteRoots) > 0; got != (session == "approved" && active) {
			t.Fatalf("session %q active=%t: received roots %v", session, active, received.GrantedWriteRoots)
		}
		if len(received.Roots) != 1 || received.Roots[0].ID != "root" {
			t.Fatal("write grant became an attached root")
		}
		if session == "other" {
			active = false
		}
	}
}
