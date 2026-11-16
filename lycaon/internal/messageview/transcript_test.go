package messageview

import (
	"reflect"
	"testing"
	"time"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestTranscriptSeparatesToolStepsFromUserProse(t *testing.T) {
	for _, tc := range []struct {
		name  string
		role  api.MessageRole
		calls []api.ToolCall
		want  string
	}{
		{"tool step", api.MessageRoleAssistant, []api.ToolCall{{ID: "call-1", Name: "wait"}}, ""},
		{"answer", api.MessageRoleAssistant, nil, "recorded prose"},
		{"progress note", api.MessageRoleAssistant, []api.ToolCall{}, "recorded prose"},
		{"user", api.MessageRoleUser, nil, "recorded prose"},
		{"tool result", api.MessageRoleTool, nil, "recorded prose"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msg := api.Message{ID: "message-1", Role: tc.role, Content: "recorded prose", ToolCalls: tc.calls}
			view := TranscriptMessage(msg)
			if view.Content != tc.want || view.ID != msg.ID || !reflect.DeepEqual(view.ToolCalls, msg.ToolCalls) {
				t.Fatalf("transcript = %+v", view)
			}
			if msg.Content != "recorded prose" || RedactMessage(msg).Content != msg.Content {
				t.Fatal("transcript projection changed model history or diagnostic capture")
			}
		})
	}
}

func TestTranscriptToolStepOmitsAlternateProseAndKeepsArgumentMasks(t *testing.T) {
	msg := api.Message{
		Role: api.MessageRoleAssistant, Content: "internal explanation",
		ContentParts: []api.MessageContentPart{{Content: "internal explanation"}},
		Grounding:    &api.CitationGrounding{Traced: true},
		ToolCalls:    []api.ToolCall{{Name: "terminal_send", Args: map[string]any{"input": "private keystrokes"}}},
	}
	view := TranscriptMessage(msg)
	if view.Content != "" || len(view.ContentParts) != 0 || view.Grounding != nil {
		t.Fatalf("tool prose remains observable: %+v", view)
	}
	if view.ToolCalls[0].Args["input"] != RedactedToolArgPlaceholder || view.HostSecretRedaction == nil {
		t.Fatalf("argument mask lost: %+v", view)
	}
	if len(msg.ContentParts) != 1 || msg.Grounding == nil || msg.ToolCalls[0].Args["input"] != "private keystrokes" {
		t.Fatal("projection mutated its source")
	}
}

// Every projection describes each action, so a row recorded before
// descriptors existed reads with them, and the stored row is left as it was.
func TestTranscriptDescribesEveryAction(t *testing.T) {
	settled := time.Date(2026, 11, 15, 8, 0, 4, 0, time.UTC)
	call := api.Message{ID: "a", Role: api.MessageRoleAssistant,
		ToolCalls: []api.ToolCall{{ID: "r1", Name: "terminal_send", Args: map[string]any{"id": "term-1", "input": "1"}}}}
	activity := api.CommandActivity{ID: "act", Label: "Run command", State: "completed", StatusCode: new(int), ExecutionIDs: []string{"e1"}}
	program := api.Message{ID: "p", Role: api.MessageRoleTool, ToolResult: &api.ToolResult{
		Tool: "command", ToolCallID: "c1", ToolArgs: map[string]any{"command": "go test ./..."}, Content: "ok",
		CommandProgram: &api.CommandProgramReceipt{ID: "prog", StartedAt: settled.Add(-4 * time.Second), SettledAt: &settled, Activities: []api.CommandActivity{activity}},
	}}
	terminal := api.Message{ID: "t", Role: api.MessageRoleTool, ToolResult: &api.ToolResult{
		Tool: "terminal_close", ToolCallID: "r2", ToolArgs: map[string]any{"id": "term-1"}, Content: "{}",
	}}
	stopped := api.Message{ID: "s", Role: api.MessageRoleTool, ToolResult: &api.ToolResult{
		Tool: "command", ToolCallID: "c2", ToolArgs: map[string]any{"command": "curl -X POST https://example.test"}, Outcome: api.ToolResultOutcomeRejected,
		CommandProgram: &api.CommandProgramReceipt{ID: "prog-2", State: "failed", StopCode: "HTTP_REQUEST_FAILED", StartedAt: settled},
	}}

	out := TranscriptMessages([]api.Message{call, program, terminal, stopped})
	if action := out[3].ToolResult.Action; action == nil || action.Status != "error" || action.Ran.Text != "curl -X POST https://example.test" {
		t.Fatalf("a program without activities has no card: %+v", action)
	}

	if action := out[0].ToolCalls[0].Action; action == nil || action.Status != "running" || action.Label != "Terminal session" {
		t.Fatalf("running call card %+v", action)
	}
	described := out[1].ToolResult.CommandProgram.Activities[0].Action
	if described == nil || described.Ran.Text != "go test ./..." || described.Status != "done" || out[1].ToolResult.Action != nil {
		t.Fatalf("activity card %+v, result card %+v", described, out[1].ToolResult.Action)
	}
	if action := out[2].ToolResult.Action; action == nil || action.FoldKey != "terminal:term-1" {
		t.Fatalf("terminal card %+v", action)
	}
	if call.ToolCalls[0].Action != nil || program.ToolResult.CommandProgram.Activities[0].Action != nil || terminal.ToolResult.Action != nil {
		t.Fatal("projection wrote descriptors into the stored rows")
	}
}

// A compound command's activity is cut from its source only while no
// redaction mark has shifted the offsets.
func TestTranscriptPassesCommandMarksToActivities(t *testing.T) {
	source := "echo [secret] && ls"
	first := api.CommandActivity{ID: "a", State: "completed", Subject: "echo", Spans: []api.CommandActivitySpan{{SourceRef: "src", StartByte: 0, EndByte: 13}}}
	second := api.CommandActivity{ID: "b", State: "completed", Subject: "list folder", Spans: []api.CommandActivitySpan{{SourceRef: "src", StartByte: 17, EndByte: 19}}}
	msg := api.Message{ID: "p", Role: api.MessageRoleTool,
		HostSecretRedaction: &api.HostSecretRedactionMeta{Spans: []api.RedactedSpan{{Field: "tool_result.tool_args.command", Start: 5, Length: 8, Kind: api.RedactionKind("secret")}}},
		ToolResult: &api.ToolResult{Tool: "command", ToolCallID: "c", ToolArgs: map[string]any{"command": source}, Content: "ok",
			CommandProgram: &api.CommandProgramReceipt{ID: "prog", SourceRef: "src", Activities: []api.CommandActivity{first, second}}}}
	if got := TranscriptMessage(msg).ToolResult.CommandProgram.Activities[1].Action.Ran.Text; got != "list folder" {
		t.Fatalf("marked source was sliced: %q", got)
	}
	msg.HostSecretRedaction = nil
	if got := TranscriptMessage(msg).ToolResult.CommandProgram.Activities[1].Action.Ran.Text; got != "ls" {
		t.Fatalf("unmarked source was not sliced: %q", got)
	}
}
