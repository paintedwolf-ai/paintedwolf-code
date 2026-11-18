package compaction

import (
	"strings"
	"unicode/utf8"
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
