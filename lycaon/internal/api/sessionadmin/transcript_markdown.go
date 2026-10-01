package sessionadmin

import (
	"fmt"
	"strings"
	"time"
	"unicode"

	wire "github.com/lycaon/lycaon/pkg/api"
)

const (
	toolResultMarkdownMaxRunes = 500
	exportTitleMaxRunes        = 80
)

// transcriptMarkdownOpts configures the human-readable export preamble.
type transcriptMarkdownOpts struct {
	Title      string
	SessionID  string
	ExportedAt time.Time
}

// renderTranscriptMarkdown turns messages into a chrome-filtered Markdown conversation.
func renderTranscriptMarkdown(messages []wire.Message, opts transcriptMarkdownOpts) string {
	exported := opts.ExportedAt
	if exported.IsZero() {
		exported = time.Now().UTC()
	} else {
		exported = exported.UTC()
	}
	title := strings.TrimSpace(opts.Title)
	if title == "" {
		title = "session"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", title)
	fmt.Fprintf(&b, "- Session: `%s`\n", strings.TrimSpace(opts.SessionID))
	fmt.Fprintf(&b, "- Exported: %s\n\n", exported.Format(time.RFC3339))

	for _, msg := range messagesForMarkdownExport(messages) {
		switch msg.Role {
		case wire.MessageRoleUser:
			b.WriteString("## You\n\n")
			writeBody(&b, msg.Content)
		case wire.MessageRoleAssistant:
			b.WriteString("## Assistant\n\n")
			if len(msg.ToolCalls) > 0 {
				for _, tc := range msg.ToolCalls {
					name := strings.TrimSpace(tc.Name)
					if name == "" {
						name = "tool"
					}
					fmt.Fprintf(&b, "> tool: %s\n\n", name)
				}
			}
			writeBody(&b, msg.Content)
		case wire.MessageRoleTool:
			name := "tool"
			if msg.ToolResult != nil && strings.TrimSpace(msg.ToolResult.Tool) != "" {
				name = strings.TrimSpace(msg.ToolResult.Tool)
			}
			fmt.Fprintf(&b, "> tool: %s\n\n", name)
			body := msg.Content
			if msg.ToolResult != nil && strings.TrimSpace(msg.ToolResult.Content) != "" {
				body = msg.ToolResult.Content
			}
			writeTruncatedFence(&b, body)
		case wire.MessageRoleSystem:
			b.WriteString("## System\n\n")
			writeBody(&b, msg.Content)
		default:
			fmt.Fprintf(&b, "## %s\n\n", string(msg.Role))
			writeBody(&b, msg.Content)
		}
	}
	return b.String()
}

// messagesForMarkdownExport removes internal rows and uncommitted draft prose.
// Visible tool calls and results retain their transcript order.
func messagesForMarkdownExport(messages []wire.Message) []wire.Message {
	out := make([]wire.Message, 0, len(messages))
	for _, msg := range messages {
		if wire.IsTranscriptChromeMessage(msg) {
			continue
		}
		if wire.IsInternalTranscriptMessage(msg) {
			continue
		}
		draftWithToolCalls := msg.Role == wire.MessageRoleAssistant &&
			msg.Kind == wire.MessageKindDraft && len(msg.ToolCalls) > 0
		if msg.Role == wire.MessageRoleTool && msg.ToolResult != nil && msg.ToolResult.Tool == "surface_note" {
			continue
		}
		if msg.Role == wire.MessageRoleAssistant && len(msg.ToolCalls) > 0 {
			calls := make([]wire.ToolCall, 0, len(msg.ToolCalls))
			for _, call := range msg.ToolCalls {
				if call.Name != "surface_note" {
					calls = append(calls, call)
				}
			}
			msg.ToolCalls = calls
		}
		if msg.Role == wire.MessageRoleAssistant && msg.Kind == wire.MessageKindDraft {
			if draftWithToolCalls {
				msg.Content = ""
			} else if msg.DraftStatus != wire.DraftStatusCommitted {
				continue
			}
		}
		if msg.Role == wire.MessageRoleAssistant && strings.TrimSpace(msg.Content) == "" && len(msg.ToolCalls) == 0 {
			continue
		}
		out = append(out, msg)
	}
	return out
}

// sanitizeTranscriptExportFilename builds "<title>-<short-id>.<ext>".
func sanitizeTranscriptExportFilename(title, sessionID, ext string) string {
	base := sanitizeExportTitle(title)
	short := strings.TrimSpace(sessionID)
	if len(short) > 8 {
		short = short[:8]
	}
	if short == "" {
		short = "session"
	}
	ext = strings.ToLower(strings.TrimSpace(ext))
	if ext == "" {
		ext = "md"
	}
	ext = strings.TrimPrefix(ext, ".")
	return fmt.Sprintf("%s-%s.%s", base, short, ext)
}

func sanitizeExportTitle(title string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		title = "session"
	}
	var b strings.Builder
	prevDash := false
	for _, r := range title {
		switch {
		case r == '/' || r == '\\' || r == 0:
			if !prevDash && b.Len() > 0 {
				b.WriteRune('-')
				prevDash = true
			}
		case unicode.IsControl(r):
			continue
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			prevDash = false
		case r == '-' || r == '_' || r == '.':
			if !prevDash && b.Len() > 0 {
				b.WriteRune('-')
				prevDash = true
			}
		case unicode.IsSpace(r):
			if !prevDash && b.Len() > 0 {
				b.WriteRune('-')
				prevDash = true
			}
		default:
			if !prevDash && b.Len() > 0 {
				b.WriteRune('-')
				prevDash = true
			}
		}
		if runeCount(b.String()) >= exportTitleMaxRunes {
			break
		}
	}
	out := strings.Trim(b.String(), "-._")
	if out == "" {
		return "session"
	}
	return out
}

func writeBody(b *strings.Builder, content string) {
	content = strings.TrimRight(content, "\n")
	if content == "" {
		b.WriteString("\n")
		return
	}
	b.WriteString(content)
	b.WriteString("\n\n")
}

func writeTruncatedFence(b *strings.Builder, content string) {
	content = strings.TrimRight(content, "\n")
	truncated := false
	runes := []rune(content)
	if len(runes) > toolResultMarkdownMaxRunes {
		content = string(runes[:toolResultMarkdownMaxRunes])
		truncated = true
	}
	b.WriteString("```\n")
	b.WriteString(content)
	if truncated {
		b.WriteString("\n…")
	}
	b.WriteString("\n```\n\n")
}

func runeCount(s string) int {
	return len([]rune(s))
}
