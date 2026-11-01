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
	if len(parts) != 4 || !strings.Contains(parts[0].Content, "missing quote\nFix:") || !strings.Contains(parts[1].Content, `"completed_effects":1`) || strings.Contains(parts[1].Content, `"feedback"`) || !strings.Contains(parts[1].Content, `"result_message_id":"message"`) || !strings.Contains(parts[2].Content, "\nnative\nerror\n\n") || !strings.Contains(parts[3].Content, "\nearlier\noutput\n\n") {
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

func TestCommandProjectionPreservesLiteralAndActualNewlines(t *testing.T) {
	message := commandResultMessage(t)
	var body map[string]any
	testutil.FailErr(t, "decode fixture", json.Unmarshal([]byte(message.Content), &body))
	literal := "literal\\n and real\nline"
	body["stdout"] = literal
	encoded, err := json.Marshal(body)
	testutil.FailErr(t, "encode literal output", err)
	message.Content = string(encoded)
	parts := projectCommandResult(message, api.MessageTextParts(message))
	if len(parts) != 4 || !strings.Contains(parts[3].Content, "\n"+literal+"\n") {
		t.Fatalf("stream bytes were decoded twice: %+v", parts)
	}
}

func TestCommandProvenanceSnapshotsAreDetached(t *testing.T) {
	message := commandResultMessage(t)
	message.ToolResult.Invocation.Failure = &api.InvocationFailure{Code: "original"}
	captured := CommandProvenance(message.ToolResult)
	*message.ToolResult.CommandProgram.StatusCode = 0
	message.ToolResult.Invocation.Failure.Code = "changed"
	restored := &api.ToolResult{Tool: "command", ToolCallID: "call"}
	captured.Restore(restored)
	if *restored.CommandProgram.StatusCode != 2 || restored.Invocation.Failure.Code != "original" {
		t.Fatalf("caller changed captured facts: %+v", restored)
	}
	restored.Invocation.Failure.Code = "modified first restore"
	captured.Restore(restored)
	if restored.Invocation.Failure.Code != "original" {
		t.Fatal("restored facts mutated provenance")
	}
}

func TestCommandProjectionRetainsStreamLineEndingsAndWhitespace(t *testing.T) {
	for _, text := range []string{"  leading and trailing  ", "first\r\nsecond\r\n", "literal\\n", "```~~~\n  "} {
		message := commandResultMessage(t)
		var body map[string]any
		testutil.FailErr(t, "decode fixture", json.Unmarshal([]byte(message.Content), &body))
		body["stdout"] = text
		raw, err := json.Marshal(body)
		testutil.FailErr(t, "encode stream fixture", err)
		message.Content = string(raw)
		projected := Project([]api.Message{message})
		want := "⟦D⟧" + strings.ReplaceAll(text, "\n", "\n⟦D⟧")
		if !strings.Contains(projected[len(projected)-1].Content, want) {
			t.Fatalf("stream bytes changed: %q / %q", text, projected[len(projected)-1].Content)
		}
	}
}
