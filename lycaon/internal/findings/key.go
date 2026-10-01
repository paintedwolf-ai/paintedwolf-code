package findings

import "strings"

func normalizeKey(sessionID string) string {
	return strings.TrimSpace(sessionID)
}
