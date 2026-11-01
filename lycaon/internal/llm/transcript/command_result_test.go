package transcript

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func commandResultMessage(t *testing.T) api.Message {
	t.Helper()
	status := 2
	body, err := json.Marshal(map[string]any{"program_id": "program", "state": "rejected", "status": status, "stdout": "earlier\noutput\n", "stderr": "native\nerror\n", "stdout_ref": "output:stdout", "executed_children": 2, "completed_effects": 1, "uncertain_effects": 0, "refusal": map[string]any{"code": "COMMAND_SYNTAX_INVALID", "feedback": "Rejected: missing quote\nFix: close the string\nCode: COMMAND_SYNTAX_INVALID", "details": map[string]any{"source_line": 2}}})
	testutil.FailErr(t, "encode command result", err)
	return api.Message{ID: "message", Role: api.MessageRoleTool, Origin: api.MessageOriginTool, Authority: api.ContentAuthorityNone, TrustTier: api.ContentTrustTierUntrusted, Content: string(body), ToolResult: &api.ToolResult{Tool: "command", ToolCallID: "call", Content: string(body), Outcome: api.ToolResultOutcomeCompleted, Invocation: &api.InvocationReceipt{ID: "invocation", Tool: "command", ToolCallID: "call", Invoked: true}, CommandProgram: &api.CommandProgramReceipt{ID: "program", ParentReceiptID: "invocation", State: "rejected", StatusCode: &status, StopCode: "COMMAND_SYNTAX_INVALID"}}}
}

func TestCommandResultProjectionPreservesTextAndCanonicalHistory(t *testing.T) {
	message := commandResultMessage(t)
	before, err := json.Marshal(message)
	testutil.FailErr(t, "capture canonical result", err)
	parts := projectCommandResult(message, api.MessageTextParts(message))
	if len(parts) != 4 || !strings.Contains(parts[0].Content, "missing quote\nFix:") || !strings.Contains(parts[1].Content, `"completed_effects":1`) || strings.Contains(parts[1].Content, `"feedback"`) || !strings.Contains(parts[1].Content, `"result_message_id":"message"`) || parts[2].Content != "Command stderr:\nnative\nerror\n" || parts[3].Content != "Command stdout:\nearlier\noutput\n" {
		t.Fatalf("command text or facts changed: %+v", parts)
	}
	for _, part := range parts {
		if part.Authority != api.ContentAuthorityNone || part.Origin != api.MessageOriginTool {
			t.Fatalf("result gained instruction authority: %+v", part)
		}
	}
	once := Project([]api.Message{message})
	twice := Project(once)
	a, err := json.Marshal(once)
	testutil.FailErr(t, "encode first projection", err)
	b, err := json.Marshal(twice)
	testutil.FailErr(t, "encode second projection", err)
	after, err := json.Marshal(message)
	testutil.FailErr(t, "capture retained result", err)
	if string(a) != string(b) || string(before) != string(after) {
		t.Fatal("projection changed history or repeated its framing")
	}
}

func TestCommandResultProjectionRequiresMatchingHostReceipts(t *testing.T) {
	for _, change := range []func(*api.Message){
		func(m *api.Message) { m.ToolResult.CommandProgram = nil },
		func(m *api.Message) { m.ToolResult.Invocation = nil },
		func(m *api.Message) { m.ToolResult.Tool = "mcp_command" },
		func(m *api.Message) { m.ToolResult.ToolCallID = "other" },
		func(m *api.Message) { m.ToolResult.Invocation.Tool = "other" },
		func(m *api.Message) { m.ToolResult.Invocation.Invoked = false },
		func(m *api.Message) { m.ToolResult.CommandProgram.ParentReceiptID = "other" },
		func(m *api.Message) { m.ToolResult.CommandProgram.ID = "other" },
		func(m *api.Message) { m.ToolResult.CommandProgram.State = "completed" },
		func(m *api.Message) { m.ToolResult.CommandProgram.StopCode = "OTHER_CODE" },
		func(m *api.Message) { n := 0; m.ToolResult.CommandProgram.StatusCode = &n },
		func(m *api.Message) { m.Origin = api.MessageOriginModel },
		func(m *api.Message) { m.ID = "" },
		func(m *api.Message) { m.Content = strings.Repeat("x", maxCommandProjectionBytes+1) },
	} {
		message := commandResultMessage(t)
		change(&message)
		parts := api.MessageTextParts(message)
		got := projectCommandResult(message, parts)
		a, _ := json.Marshal(got)
		b, _ := json.Marshal(parts)
		if string(a) != string(b) {
			t.Fatalf("unbound content was projected: %s", a)
		}
	}
}
