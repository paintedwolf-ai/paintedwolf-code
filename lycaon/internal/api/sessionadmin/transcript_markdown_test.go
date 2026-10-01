package sessionadmin

import (
	"strings"
	"testing"
	"time"

	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestRenderTranscriptMarkdownDropsChromeAndLabelsRoles(t *testing.T) {
	t.Parallel()
	msgs := []wire.Message{
		{Role: wire.MessageRoleUser, Content: "hello", Visibility: wire.MessageVisibilityTranscript},
		{Role: wire.MessageRoleAssistant, Content: "hi\n\n```go\nfmt.Println(1)\n```", Visibility: wire.MessageVisibilityTranscript},
		{
			Role:       wire.MessageRoleAssistant,
			ToolCalls:  []wire.ToolCall{{ID: "c1", Name: "read"}},
			Visibility: wire.MessageVisibilityTranscript,
		},
		{
			Role:       wire.MessageRoleTool,
			Content:    "file body",
			ToolResult: &wire.ToolResult{Tool: "read", ToolCallID: "c1", Content: "file body"},
			Visibility: wire.MessageVisibilityTranscript,
		},
		{Role: wire.MessageRoleSystem, Kind: wire.MessageKindProgressComplete, Content: "chrome", ProgressComplete: &wire.ProgressCompleteMeta{}},
		{Role: wire.MessageRoleUser, Content: "hidden nudge", Visibility: wire.MessageVisibilityInternal},
	}
	md := renderTranscriptMarkdown(msgs, transcriptMarkdownOpts{
		Title:      "Demo Chat",
		SessionID:  "abcdef12-3456-7890",
		ExportedAt: time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC),
	})
	if !strings.Contains(md, "# Demo Chat") {
		t.Fatalf("missing title header: %s", md)
	}
	if !strings.Contains(md, "## You") || !strings.Contains(md, "hello") {
		t.Fatalf("missing user turn: %s", md)
	}
	if !strings.Contains(md, "## Assistant") || !strings.Contains(md, "```go") {
		t.Fatalf("missing assistant / code fence: %s", md)
	}
	if !strings.Contains(md, "> tool: read") {
		t.Fatalf("missing tool summary: %s", md)
	}
	if strings.Contains(md, "chrome") || strings.Contains(md, "hidden nudge") {
		t.Fatalf("chrome/internal leaked: %s", md)
	}
	if !strings.Contains(md, "Exported: 2026-07-28T12:00:00Z") {
		t.Fatalf("missing export timestamp: %s", md)
	}
}

func TestRenderTranscriptMarkdownHidesDraftProseButKeepsActions(t *testing.T) {
	t.Parallel()
	msgs := []wire.Message{
		{
			Role:        wire.MessageRoleAssistant,
			Kind:        wire.MessageKindDraft,
			DraftStatus: wire.DraftStatusCommitted,
			Content:     "private orchestration prose",
			ToolCalls:   []wire.ToolCall{{ID: "c1", Name: "read"}},
			Visibility:  wire.MessageVisibilityTranscript,
		},
		{
			Role:       wire.MessageRoleAssistant,
			ToolCalls:  []wire.ToolCall{{ID: "c-note", Name: "surface_note"}},
			Visibility: wire.MessageVisibilityTranscript,
		},
		{
			Role:       wire.MessageRoleTool,
			Content:    `{"status":"noted"}`,
			ToolResult: &wire.ToolResult{Tool: "surface_note", ToolCallID: "c-note", Content: `{"status":"noted"}`},
			Visibility: wire.MessageVisibilityTranscript,
		},
		{
			Role:       wire.MessageRoleAssistant,
			Kind:       wire.MessageKindAgentNote,
			Content:    "visible verified note",
			Visibility: wire.MessageVisibilityTranscript,
		},
		{
			Role:        wire.MessageRoleAssistant,
			Kind:        wire.MessageKindDraft,
			DraftStatus: wire.DraftStatusRejected,
			Content:     "rejected private attempt",
			ToolCalls:   []wire.ToolCall{{ID: "c2", Name: "grep"}},
			Visibility:  wire.MessageVisibilityTranscript,
		},
		{
			Role:       wire.MessageRoleTool,
			Content:    "match",
			ToolResult: &wire.ToolResult{Tool: "grep", ToolCallID: "c2", Content: "match"},
			Visibility: wire.MessageVisibilityTranscript,
		},
		{
			Role:        wire.MessageRoleAssistant,
			Kind:        wire.MessageKindDraft,
			DraftStatus: wire.DraftStatusCommitted,
			Content:     "accepted final answer",
			Visibility:  wire.MessageVisibilityTranscript,
		},
	}
	md := renderTranscriptMarkdown(msgs, transcriptMarkdownOpts{Title: "Demo", SessionID: "s1"})
	if strings.Contains(md, "private orchestration prose") || strings.Contains(md, "rejected private attempt") {
		t.Fatalf("draft prose leaked into Markdown export: %s", md)
	}
	if !strings.Contains(md, "> tool: read") {
		t.Fatalf("draft action missing from Markdown export: %s", md)
	}
	if !strings.Contains(md, "> tool: grep") {
		t.Fatalf("rejected draft action missing from Markdown export: %s", md)
	}
	if !strings.Contains(md, "accepted final answer") {
		t.Fatalf("accepted answer missing from Markdown export: %s", md)
	}
	if strings.Contains(md, "surface_note") || strings.Contains(md, `{"status":"noted"}`) {
		t.Fatalf("surface_note transport leaked into Markdown export: %s", md)
	}
	if !strings.Contains(md, "visible verified note") {
		t.Fatalf("agent note missing from Markdown export: %s", md)
	}
}

func TestSanitizeTranscriptExportFilename(t *testing.T) {
	t.Parallel()
	got := sanitizeTranscriptExportFilename(`My / weird\title`, "abcdef12-3456-7890", "md")
	if got != "My-weird-title-abcdef12.md" {
		t.Fatalf("got %q", got)
	}
	got = sanitizeTranscriptExportFilename("", "xyz", "json")
	if got != "session-xyz.json" {
		t.Fatalf("empty title got %q", got)
	}
}
