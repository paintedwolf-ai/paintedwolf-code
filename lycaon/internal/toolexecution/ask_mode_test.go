package toolexecution

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/platform"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
)

func TestAskExecutorAdmitsOnlyRegisteredReadTools(t *testing.T) {
	for _, tc := range []struct {
		name       string
		registered bool
		mcpRead    bool
		allowed    bool
	}{
		{name: "inspect_source", registered: true, allowed: true},
		{name: "write", registered: true},
		{name: "mcp_fixture_read", registered: true, mcpRead: true, allowed: true},
		{name: "mcp_fixture_mutation", registered: true},
		{name: "mcp_fixture_missing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := tools.NewDefaultRegistry()
			calls := 0
			handler := func(context.Context, map[string]any, tools.ToolContext) (string, error) {
				calls++
				return "observation", nil
			}
			if tc.registered {
				meta := tools.ToolMeta{Name: tc.name, ReadOnlyHint: tc.mcpRead, ArgsSchema: map[string]any{"type": "object"}}
				if meta.IsMCP() {
					meta.Source, meta.SourceID = tools.ToolSourceMCP, "fixture"
					testutil.FailErr(t, "register MCP tool", reg.RegisterDefinition(tools.Definition{
						Meta: meta, Contract: toolcontract.External("mcp:fixture"), Handler: handler,
					}))
				} else {
					testutil.FailErr(t, "register native tool", reg.Register(tc.name, handler))
				}
			}
			executor := NewExecutor(nil, reg, "implement")
			ctx := fixtureToolContext(t.TempDir())
			ctx.Source.AskMode, ctx.Source.ReadOnly = true, true

			out, err := executor.Invoke(t.Context(), tc.name, map[string]any{"operation": "git_head"}, ctx)
			if tc.allowed {
				testutil.FailErr(t, "run read tool", err)
				if calls != 1 || out != "observation" {
					t.Fatalf("read tool calls = %d, output = %q", calls, out)
				}
				return
			}
			reject := toolrejection.AsToolReject(err)
			if reject == nil || reject.Code != tools.AskModeActiveCode || calls != 0 || out != "" {
				t.Fatalf("denied tool calls = %d, output = %q, error = %v", calls, out, err)
			}
		})
	}
}

func TestReadOnlyProgramSurfaceNarrowsTheDeclaredProfile(t *testing.T) {
	const profile = "fixture"
	boundary := sandbox.NewBoundary(sandbox.Config{}, []sandbox.ToolProfile{
		{ID: profile, CommandEffects: toolcontract.AllCommandEffects()},
	})
	executor := NewExecutor(toolprofiles.NewProfilePolicyEngine(boundary), tools.NewDefaultRegistry(), profile)
	read := nativeDefinition(t, "read")
	write := nativeDefinition(t, "write")
	surface, err := executor.Metadata.FreezeProgramSurface(t.Context(), platform.ToolFilter{ProfileID: profile}, []toolcontract.OperationDefinition{read, write})
	testutil.FailErr(t, "freeze declared program surface", err)

	invocation, _ := frameFixture(t)
	readOnly := surface.ReadOnly()
	for _, binding := range []struct {
		def        toolcontract.OperationDefinition
		wantDenied bool
	}{
		{def: read},
		{def: write, wantDenied: true},
	} {
		form := binding.def.Facts().Commands[0]
		operation, err := toolcontract.BindOperation(binding.def, invocation, form.Name, form.Effects[0].Mode, nil)
		testutil.FailErr(t, "bind catalog operation", err)
		testutil.FailErr(t, "validate full profile", surface.ValidateOperation(operation))
		err = readOnly.ValidateOperation(operation)
		if binding.wantDenied {
			if reject := toolrejection.AsToolReject(err); reject == nil || reject.Code != "COMMAND_EFFECT_NOT_ALLOWED" {
				t.Fatalf("write-capable operation admitted by read-only surface: %v", err)
			}
		} else {
			testutil.FailErr(t, "validate read operation", err)
		}
	}
	full := toolcontract.AllCommandEffects()
	if surface.Effects() != full || readOnly.AllowsEffects(full) {
		t.Fatalf("full effects = %v, read-only effects = %v", surface.Effects(), readOnly.Effects())
	}
}
