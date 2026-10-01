package llm

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/curationctx"
)

// CurationFocus orients the lite curator model. CacheKey excludes Task so identical
// snapshots reuse curation; PromptFocus includes Task when present.
type CurationFocus struct {
	Tool   string
	View   string
	Target string
	Task   string
}

// CacheKey fingerprints tool/view/target for curation cache lookup.
func (f CurationFocus) CacheKey() string {
	return strings.Join([]string{
		strings.TrimSpace(f.Tool),
		strings.TrimSpace(f.View),
		strings.TrimSpace(f.Target),
	}, "|")
}

// PromptFocus is the focus line rendered into the curator user prompt.
func (f CurationFocus) PromptFocus() string {
	var head strings.Builder
	if tool := strings.TrimSpace(f.Tool); tool != "" {
		head.WriteString(tool)
	}
	if view := strings.TrimSpace(f.View); view != "" {
		if head.Len() > 0 {
			head.WriteByte(' ')
		}
		head.WriteString(view)
	}
	if target := strings.TrimSpace(f.Target); target != "" {
		if head.Len() > 0 {
			head.WriteString(": ")
		}
		head.WriteString(target)
	}
	out := head.String()
	if task := strings.TrimSpace(f.Task); task != "" {
		if out != "" {
			out += "\n"
		}
		out += "Task: " + task
	}
	return out
}

func SearchFocus(ctx context.Context, tool, target string) CurationFocus {
	return curationFocus(ctx, tool, "matches", target)
}

func curationFocus(ctx context.Context, tool, view, target string) CurationFocus {
	return CurationFocus{
		Tool:   tool,
		View:   view,
		Target: target,
		Task:   curationctx.TaskHint(ctx),
	}
}
