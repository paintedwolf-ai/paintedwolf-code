package assembly

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestExecutionStateRefreshesForCurrentIntent(t *testing.T) {
	_, assembler := newAssemblyDomains(AssemblyDeps{
		Injects: promptstest.InjectRenderer(t),
		ImplementSessionState: func(context.Context, *api.Session) surface.ImplementSessionState {
			return surface.ImplementSessionState{VerifyCommand: "project-check"}
		},
	}, nil)
	sess := &api.Session{ID: "sess-inject-test", AgentType: "coordinator"}
	history := []api.Message{{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{
		Tool: "command", ToolCallID: "call", ToolArgs: map[string]any{"command": "missing-check", "cwd": "."},
		Invocation: &api.InvocationReceipt{ID: "receipt", Tool: "command", ToolCallID: "call", Status: api.InvocationStatusRejected, Failure: &api.InvocationFailure{Code: "COMMAND_NOT_FOUND"}},
	}}}
	turn := &TurnAssemblyScratch{}
	blocks, err := assembler.buildTailSystemInjects(t.Context(), sess, true, inject.CoordinatorTurnFrame{}, nil, history, turn)
	testutil.FailErr(t, "render execution state", err)
	if len(blocks) != 1 || !strings.Contains(blocks[0].Content, "COMMAND_NOT_FOUND") || !strings.Contains(blocks[0].Content, "project-check") {
		t.Fatalf("missing current receipt or declared command: %+v", blocks)
	}
	if blocks[0].Origin != api.MessageOriginHost || blocks[0].Authority != api.ContentAuthorityNone {
		t.Fatalf("receipt gained instruction authority: %+v", blocks[0])
	}
	history = append(history, api.Message{Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Content: "New request"})
	blocks, err = assembler.buildTailSystemInjects(t.Context(), sess, true, inject.CoordinatorTurnFrame{}, nil, history, turn)
	testutil.FailErr(t, "refresh execution state", err)
	if len(blocks) != 0 {
		t.Fatalf("prior intent's execution state survived: %+v", blocks)
	}
}
