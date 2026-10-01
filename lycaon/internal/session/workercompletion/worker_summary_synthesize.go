package workercompletion

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/pkg/api"
)

const maxSynthesizedSurveyChars = 3500
const maxToolExcerptChars = 900

// synthesizedSurveyHeading opens the section. The body under it comes from the
// worker-survey-preamble partial.
const synthesizedSurveyHeading = "## Synthesized survey"

// SynthesizeSummaryFromChildMessages builds a bounded survey from child tool results.
func SynthesizeSummaryFromChildMessages(msgs []api.Message, agentType string) string {
	excerpts := collectToolExcerpts(msgs, maxSynthesizedSurveyChars)
	if excerpts == "" {
		return ""
	}
	preamble, err := guidance.RenderSynthesizedSurveyPreamble(context.Background(), agentType)
	if err != nil || strings.TrimSpace(preamble) == "" {
		// The heading alone on a render fault: restating the partial's body here
		// would be a second copy of it, drifting the moment either changes.
		preamble = synthesizedSurveyHeading
	}
	out := strings.TrimSpace(preamble + "\n\n## Tool excerpts\n" + excerpts)
	if len(out) > maxSynthesizedSurveyChars {
		out = out[:maxSynthesizedSurveyChars] + "\n…"
	}
	return out
}

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
