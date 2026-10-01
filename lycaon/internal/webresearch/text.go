package webresearch

import (
	"strings"

	"github.com/lycaon/lycaon/internal/runeclamp"
)

// truncateText caps provider-supplied text at maxRunes, marking the cut. Runes,
// not bytes, so non-ASCII titles are never cut mid-rune.
func truncateText(s string, maxRunes int) string {
	return runeclamp.Clamp(strings.TrimSpace(s), maxRunes)
}
