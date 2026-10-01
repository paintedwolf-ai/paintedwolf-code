package worker

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/runeclamp"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/pkg/api"
)

func buildConflictSummary(c PromoteConflict) []string {
	var out []string
	maxLines := tooloutput.MaxInlineConflictSummaryLines
	for i, h := range c.Hunks {
		if i >= maxLines {
			out = append(out, "…")
			break
		}
		if line := formatHunkSummaryLine(h); line != "" {
			out = append(out, line)
		}
	}
	if len(out) == 0 {
		if delta := strings.TrimSpace(BranchDelta(c.Primary, c.Branch)); delta != "" {
			for i, line := range strings.Split(delta, "\n") {
				if i >= maxLines-1 {
					out = append(out, "…")
					break
				}
				line = strings.TrimSpace(line)
				if line != "" {
					out = append(out, line)
				}
			}
		}
	}
	return out
}

func formatHunkSummaryLine(h api.WorkerMergeHunk) string {
	end := h.EndLine
	if end <= 0 {
		end = h.StartLine
	}
	rangeLabel := fmt.Sprintf("L%d", h.StartLine)
	if end > h.StartLine {
		rangeLabel = fmt.Sprintf("L%d-%d", h.StartLine, end)
	}
	var parts []string
	if line := strings.TrimSpace(h.Primary); line != "" {
		parts = append(parts, "primary: "+truncateConflictLine(line))
	}
	if line := strings.TrimSpace(h.Branch); line != "" {
		parts = append(parts, "branch: "+truncateConflictLine(line))
	}
	if len(parts) == 0 {
		return rangeLabel
	}
	return rangeLabel + ": " + strings.Join(parts, " | ")
}

func truncateConflictLine(s string) string {
	const maxBytes = 120
	return runeclamp.ClampBytes(strings.TrimSpace(s), maxBytes)
}
