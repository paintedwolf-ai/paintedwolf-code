package search

import (
	"regexp"
	"strings"

	"github.com/lycaon/lycaon/internal/timelayout"
	"github.com/lycaon/lycaon/pkg/api"
)

// messageSnippetMaxRunes caps stored previews.
const messageSnippetMaxRunes = 600

// injectSentinelLine matches host marker lines.
var injectSentinelLine = regexp.MustCompile(`(?m)^[ \t]*<!--[ \t]*[A-Za-z0-9._-]+:v[0-9]+[ \t]*-->[ \t]*\r?\n?`)

// stripInjectSentinels removes host markers from indexed text.
func stripInjectSentinels(content string) string {
	return strings.TrimSpace(injectSentinelLine.ReplaceAllString(content, ""))
}

// ProjectMessageText projects visible conversation text into search rows.
func ProjectMessageText(projectID, sessionID string, msg api.Message) []IndexRow {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return nil
	}
	if msg.Role != api.MessageRoleUser && msg.Role != api.MessageRoleAssistant {
		return nil
	}
	if api.IsWorkflowBoundaryMessage(msg) ||
		api.IsProgressCompleteMessage(msg) ||
		api.IsProgressUpdateMessage(msg) {
		return nil
	}
	content := stripInjectSentinels(msg.Content)
	if content == "" {
		return nil
	}
	snippet, truncated := capRunes(content, messageSnippetMaxRunes)
	role := messageRole(msg)
	// Internal assistant prose is indexed as draft-only.
	if api.IsInternalTranscriptMessage(msg) {
		if msg.Role != api.MessageRoleAssistant {
			return nil
		}
		role = string(api.MessageKindDraft)
	}
	return []IndexRow{{
		ID:        RowID(SourceMessage, msg.ID, "text"),
		ProjectID: projectID,
		Source:    SourceMessage,
		HitKind:   HitKindMessage,
		SessionID: sessionID,
		MessageID: msg.ID,
		SourceRef: msg.ID,
		Kind:      HitKindMessage,
		Role:      role,
		Snippet:   snippet,
		Truncated: truncated,
		TS:        timelayout.Format(msg.CreatedAt),
	}}
}

func capRunes(s string, max int) (string, bool) {
	runes := []rune(s)
	if len(runes) <= max {
		return s, false
	}
	return strings.TrimSpace(string(runes[:max])), true
}
