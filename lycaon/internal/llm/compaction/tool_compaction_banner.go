package compaction

import (
	"strings"

	"github.com/lycaon/lycaon/internal/hostmarker"
)

// withoutPriorCompactionBanner replaces presentation metadata, never the JSON observation.
func withoutPriorCompactionBanner(content string) string {
	prefix, body, suffix, ok := hostmarker.SplitToolJSONBody(content)
	if !ok {
		return content
	}
	start := strings.Index(prefix, hostmarker.CompactionBannerOpen)
	if start < 0 {
		return content
	}
	end := strings.Index(prefix[start:], hostmarker.CompactionBannerClose)
	if end < 0 {
		return content
	}
	prefix = prefix[:start] + prefix[start+end+len(hostmarker.CompactionBannerClose):]
	return strings.TrimSpace(prefix) + "\n" + body + suffix
}
