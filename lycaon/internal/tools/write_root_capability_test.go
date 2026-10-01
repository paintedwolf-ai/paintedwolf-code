package tools

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

func TestWriteRootPreflightUsesCompiledCapability(t *testing.T) {
	for _, tool := range []string{"command", "terminal_open", "catalog_tool"} {
		for _, enabled := range []bool{false, true} {
			root := t.TempDir()
			calls := 0
			executor := &DefaultToolExecutor{writeRootPreflight: func(_ context.Context, gotTool string, _ map[string]any, _ ToolContext, gotRoot string) (bool, bool, string, error) {
				calls++
				if gotTool != tool || gotRoot != root {
					t.Fatalf("preflight = %q, %q; want %q, %q", gotTool, gotRoot, tool, root)
				}
				return true, false, "", nil
			}}
			tc := ToolContext{}
			if enabled {
				tc.Invocation.Contract.Capabilities = toolcontract.CapabilityWriteRoot
			}
			err := executor.preflightWriteRoot(t.Context(), tool, map[string]any{
				"capability_request": map[string]any{"write_root": root},
			}, &tc)
			testutil.FailErr(t, "preflight "+tool, err)
			if (calls == 1) != enabled {
				t.Fatalf("%s capability=%v called preflight %d times", tool, enabled, calls)
			}
		}
	}
}

func TestTerminalWriteRootCannotAuthorizeInstructionChanges(t *testing.T) {
	executor := &DefaultToolExecutor{writeRootPreflight: func(context.Context, string, map[string]any, ToolContext, string) (bool, bool, string, error) {
		t.Fatal("instruction changes reached ordinary write-root authorization")
		return true, false, "", nil
	}}
	project := t.TempDir()
	tc := ToolContext{
		Invocation:   Invocation{Contract: toolcontract.Contract{Capabilities: toolcontract.CapabilityWriteRoot}},
		Roots:        []projectroot.RootRef{{ID: "root", Path: project, IsPrimary: true}},
		ActiveRootID: "root",
	}
	err := executor.preflightWriteRoot(t.Context(), "terminal_open", map[string]any{
		"capability_request": map[string]any{"write_root": filepath.Join(project, "AGENTS.md")},
	}, &tc)
	if reject := AsToolReject(err); reject == nil || reject.Code != "POLICY_WRITE_REQUIRES_COMMAND" {
		t.Fatalf("terminal policy write = %v, want policy-write refusal", err)
	}
	if len(tc.PolicyWriteGrants) != 0 {
		t.Fatal("refused terminal received policy-write authority")
	}
}
