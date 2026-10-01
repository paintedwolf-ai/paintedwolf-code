package messageview

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestContentProjectionRetainsExactScreenedRevision(t *testing.T) {
	output := strings.Repeat("🌲a\n", 10000)
	original := api.Message{ID: "result", Role: api.MessageRoleTool, Content: output, ToolResult: &api.ToolResult{ToolCallID: "call", Tool: "read", Content: output, ToolArgs: map[string]any{"path": "src/main.go", "large": strings.Repeat("x", 10000)}}}
	before, err := json.Marshal(original)
	if err != nil {
		testutil.FailErr(t, "project retained content", err)
	}
	view := TranscriptMessage(original)
	if view.Content != "" || view.ToolResult.ContentRef == nil || view.ToolResult.ToolArgsRef == nil {
		t.Fatalf("content references missing: %+v", view.ToolResult)
	}
	if len(view.ToolResult.Content) > InlineContentBytes || !utf8.ValidString(view.ToolResult.Content) {
		t.Fatal("preview is oversized or splits Unicode")
	}
	if view.ToolResult.DisplayTitle != "src/main.go" {
		t.Fatalf("subtitle lost: %q", view.ToolResult.DisplayTitle)
	}
	for _, ref := range []*api.ChatContentReference{view.ToolResult.ContentRef, view.ToolResult.ToolArgsRef} {
		text, _, err := RetainedContent(original, ref.Field, ref.ToolCallID)
		if err != nil {
			testutil.FailErr(t, "project retained content", err)
		}
		resolved := ContentReference(ref.Field, ref.ToolCallID, text)
		if resolved.SHA256 != ref.SHA256 || resolved.TotalRunes != ref.TotalRunes {
			t.Fatal("preview reference differs from retained content")
		}
	}
	if !reflect.DeepEqual(view, TranscriptMessage(view)) {
		t.Fatal("observer projection is not idempotent")
	}
	after, err := json.Marshal(original)
	if err != nil {
		testutil.FailErr(t, "project retained content", err)
	}
	if string(before) != string(after) {
		t.Fatal("projection changed recorded history")
	}
}

func TestContentArgumentsMaskTerminalInputBeforeReferencing(t *testing.T) {
	msg := api.Message{ToolResult: &api.ToolResult{Tool: "terminal_send", ToolCallID: "call", ToolArgs: map[string]any{"input": "private input", "other": strings.Repeat("x", 10000)}}}
	view := TranscriptMessage(msg)
	body, _, err := RetainedContent(msg, "tool_args", "call")
	if err != nil {
		testutil.FailErr(t, "project retained content", err)
	}
	if strings.Contains(body, "private input") || strings.Contains(view.ToolResult.ToolArgsRef.PreviewRows[0].Text, "private input") {
		t.Fatal("observer content leaked terminal input")
	}
	if !strings.Contains(body, RedactedToolArgPlaceholder) {
		t.Fatal("observer mask missing")
	}
}

func TestResolvedToolSubjectSurvivesRetainedArgumentsAndReload(t *testing.T) {
	original := api.Message{ID: "result", Role: api.MessageRoleTool, ToolResult: &api.ToolResult{
		Tool: "command_output", ToolCallID: "call", DisplaySubject: "./task den:test:fast",
		ToolArgs: map[string]any{"handle": "7441efdd-29d7-4cd9-899e-3c542d62d8ba", "cursor": 0, "large": strings.Repeat("x", 10000)},
		Content:  "done",
	}}
	wire, err := json.Marshal(original)
	testutil.FailErr(t, "persist tool result", err)
	var restored api.Message
	testutil.FailErr(t, "reload tool result", json.Unmarshal(wire, &restored))
	view := TranscriptMessage(restored)
	if view.ToolResult.DisplayTitle != "./task den:test:fast" || view.ToolResult.ToolArgsRef == nil {
		t.Fatalf("lost resolved subject: %+v", view.ToolResult)
	}
	args, _, err := RetainedContent(restored, "tool_args", "call")
	testutil.FailErr(t, "read retained arguments", err)
	if !strings.Contains(args, "7441efdd-29d7-4cd9-899e-3c542d62d8ba") {
		t.Fatal("handle missing from retained information")
	}
	if !reflect.DeepEqual(view, TranscriptMessage(view)) {
		t.Fatal("projection changed on replay")
	}
}
