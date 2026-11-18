package compaction

import (
	"strings"
	"unicode/utf8"

	"github.com/lycaon/lycaon/pkg/api"
)

const continuationExcerptBytes = 192

func recordedCommandText(rows []ContextMessage) map[string]string {
	commands := make(map[string]string)
	for _, row := range rows {
		for _, call := range row.ToolCalls {
			if call.Name != "command" || call.ID == "" {
				continue
			}
			if command, ok := call.Args["command"].(string); ok {
				commands[call.ID] = continuationExcerpt(command)
			}
		}
	}
	return commands
}

func recordedCompletionOpening(message api.Message) string {
	if message.Kind != api.MessageKindCompletionReport || message.Role != api.MessageRoleAssistant {
		return ""
	}
	if message.CompactedChunk == nil {
		if body := continuationExcerpt(message.Content); body != "" {
			return body
		}
	}
	if report := message.CompletionReport; report != nil {
		if headline := continuationExcerpt(report.Headline); headline != "" {
			return headline
		}
		return continuationExcerpt(report.Summary)
	}
	return ""
}

func continuationExcerpt(text string) string {
	text = strings.TrimSpace(text)
	if len(text) <= continuationExcerptBytes {
		return text
	}
	end := continuationExcerptBytes - len("…")
	for !utf8.RuneStart(text[end]) {
		end--
	}
	return text[:end] + "…"
}
