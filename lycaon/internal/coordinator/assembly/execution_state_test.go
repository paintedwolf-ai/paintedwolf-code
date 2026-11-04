package assembly

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/messageview"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
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
	blocks := assembler.appendExecutionState(t.Context(), sess, true, inject.CoordinatorTurnFrame{}, history, turn, nil)
	if len(blocks) != 1 || !strings.Contains(blocks[0].Content, "COMMAND_NOT_FOUND") || !strings.Contains(blocks[0].Content, "project-check") {
		t.Fatalf("missing current receipt or declared command: %+v", blocks)
	}
	if blocks[0].Origin != api.MessageOriginHost || blocks[0].ContentParts[1].Authority != api.ContentAuthorityNone {
		t.Fatalf("receipt gained instruction authority: %+v", blocks[0])
	}
	history = append(history, api.Message{Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Content: "New request"})
	blocks = assembler.appendExecutionState(t.Context(), sess, true, inject.CoordinatorTurnFrame{}, history, turn, nil)
	if len(blocks) != 0 {
		t.Fatalf("prior intent's execution state survived: %+v", blocks)
	}
}

func toolRow(ord int64, batch, call, tool string, status api.InvocationStatus, code string) api.Message {
	receipt := &api.InvocationReceipt{ID: "receipt-" + call, Tool: tool, ToolCallID: call, ArgsDigest: call, Status: status, Invoked: status == api.InvocationStatusCompleted}
	if code != "" {
		receipt.Failure = &api.InvocationFailure{Code: code}
	}
	return api.Message{ID: call, Ord: ord, Role: api.MessageRoleTool, ToolResult: &api.ToolResult{
		Tool: tool, ToolCallID: call, AssistantMessageID: batch, Invocation: receipt, ToolArgs: map[string]any{"path": "a.go"},
	}}
}

// The prompt history is compacted away; the block still follows canonical rows.
func TestExecutionStateReadsCanonicalRowsNotRetainedHistory_ACEXEC04(t *testing.T) {
	canonical := []api.Message{
		{ID: "user", Ord: 1, Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Content: "go"},
		toolRow(2, "batch-failed", "failed", "write", api.InvocationStatusRejected, "MUTATION_BROKE_PARSE"),
	}
	reader := func(_ context.Context, _ string, window messageview.MessageWindow) ([]api.Message, error) {
		end := len(canonical)
		if window.BeforeOrd > 0 {
			end = int(window.BeforeOrd) - 1
		}
		return canonical[max(0, end-window.Limit):end], nil
	}
	compacted := []api.Message{{ID: "summary", Role: api.MessageRoleAssistant, Content: "summary"}}
	sess := &api.Session{ID: "sess-inject-test", AgentType: "coordinator"}
	for age := range 5 {
		if age > 0 {
			id := fmt.Sprintf("later-%d", age)
			canonical = append(canonical, toolRow(int64(len(canonical)+1), id, id, "read", api.InvocationStatusCompleted, ""))
		}
		// A new assembler each pass: nothing carries over in process.
		_, assembler := newAssemblyDomains(AssemblyDeps{Injects: promptstest.InjectRenderer(t), ReadMessageWindow: reader}, nil)
		blocks := assembler.appendExecutionState(t.Context(), sess, true, inject.CoordinatorTurnFrame{}, compacted, &TurnAssemblyScratch{}, nil)
		if live := age < inject.MaxRejectedOperationStepAge; (len(blocks) == 1) != live {
			t.Fatalf("age %d: blocks=%d", age, len(blocks))
		}
	}
}

func TestExecutionStateOmitsBlockWhenWindowUnreadable(t *testing.T) {
	_, assembler := newAssemblyDomains(AssemblyDeps{
		Injects: promptstest.InjectRenderer(t),
		ReadMessageWindow: func(context.Context, string, messageview.MessageWindow) ([]api.Message, error) {
			return nil, errors.New("store offline")
		},
	}, nil)
	history := []api.Message{toolRow(1, "batch", "failed", "write", api.InvocationStatusRejected, "MUTATION_BROKE_PARSE")}
	sess := &api.Session{ID: "sess-inject-test", AgentType: "coordinator"}
	if blocks := assembler.appendExecutionState(t.Context(), sess, true, inject.CoordinatorTurnFrame{}, history, &TurnAssemblyScratch{}, nil); len(blocks) != 0 {
		t.Fatalf("unreadable window rendered stale rows: %+v", blocks)
	}
}
