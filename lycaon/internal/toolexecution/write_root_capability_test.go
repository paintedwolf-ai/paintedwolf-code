package toolexecution

import (
	"context"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
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
			executor := func() *Executor {
				e := NewExecutor(nil, nil, "")
				e.Boundary.writeRootPreflight = func(_ context.Context, gotTool string, _ map[string]any, _ tools.ToolContext, gotRoot string) (bool, bool, string, error) {
					calls++
					if gotTool != tool || gotRoot != root {
						t.Fatalf("preflight = %q, %q; want %q, %q", gotTool, gotRoot, tool, root)
					}
					return true, false, "", nil
				}
				return e
			}()
			tc := tools.ToolContext{}
			if enabled {
				tc.Invocation.Contract.Capabilities = toolcontract.CapabilityWriteRoot
			}
			err := executor.Boundary.preflightWriteRoot(t.Context(), tool, map[string]any{
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
	executor := func() *Executor {
		e := NewExecutor(nil, nil, "")
		e.Boundary.writeRootPreflight = func(context.Context, string, map[string]any, tools.ToolContext, string) (bool, bool, string, error) {
			t.Fatal("instruction changes reached ordinary write-root authorization")
			return true, false, "", nil
		}
		return e
	}()
	project := t.TempDir()
	contract, ok := toolcontract.Lookup("terminal_open")
	if !ok {
		t.Fatal("compiled terminal contract unavailable")
	}
	tc := tools.ToolContext{
		Invocation: tools.Invocation{Contract: contract},
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "root", Path: project, IsPrimary: true}},
			ActiveRootID: "root"},
	}
	err := executor.Boundary.preflightWriteRoot(t.Context(), "terminal_open", map[string]any{
		"capability_request": map[string]any{"write_root": filepath.Join(project, "AGENTS.md")},
	}, &tc)
	if reject := toolrejection.AsToolReject(err); reject == nil || reject.Code != "POLICY_WRITE_REQUIRES_COMMAND" {
		t.Fatalf("terminal policy write = %v, want policy-write refusal", err)
	}
	if len(tc.Files.PolicyWriteGrants) != 0 {
		t.Fatal("refused terminal received policy-write authority")
	}
}
