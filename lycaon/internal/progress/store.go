package progress

import (
	"context"
	"strings"
)

// ContentMaxChars bounds the stored progress doc.
const ContentMaxChars = 8000

// Store persists the coordinator-authored progress doc keyed by root session id.
type Store interface {
	Set(sessionID, content string) error
	Get(ctx context.Context, sessionID string) string
}

func normalizeKey(sessionID string) string {
	return strings.TrimSpace(sessionID)
}

func clampContent(content string) string {
	content = strings.TrimSpace(content)
	if runes := []rune(content); len(runes) > ContentMaxChars {
		return string(runes[:ContentMaxChars])
	}
	return content
}
