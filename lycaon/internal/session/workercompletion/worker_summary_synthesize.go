package workercompletion

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

const maxToolExcerptChars = 900

func collectToolExcerpts(msgs []api.Message, maxTotal int) string {
	if len(msgs) == 0 || maxTotal <= 0 {
		return ""
	}
	var blocks []string
	used := 0
	seen := make(map[string]struct{})
	for i := len(msgs) - 1; i >= 0 && used < maxTotal; i-- {
		msg := msgs[i]
		if msg.Role != api.MessageRoleTool {
			continue
		}
		content := strings.TrimSpace(msg.Content)
		if content == "" || toolMessageFailed(msg) {
			continue
		}
		if len(content) < 24 {
			continue
		}
		key := content
		if len(key) > 64 {
			key = key[:64]
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		chunk := content
		if len(chunk) > maxToolExcerptChars {
			chunk = chunk[:maxToolExcerptChars] + "\n…"
		}
		blocks = append(blocks, chunk)
		used += len(chunk)
	}
	if len(blocks) == 0 {
		return ""
	}
	var b strings.Builder
	for i, block := range blocks {
		if i > 0 {
			b.WriteString("\n\n---\n\n")
		}
		fmt.Fprintf(&b, "### Excerpt %d\n%s", i+1, block)
	}
	return strings.TrimSpace(b.String())
}
