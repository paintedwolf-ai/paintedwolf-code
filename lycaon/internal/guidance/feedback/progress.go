package feedback

import (
	"fmt"
	"strings"
)

const defaultWorkflowNext = "Advance when gate satisfied"

// FormatWorkflowProgress renders a workflow gate progress line.
func FormatWorkflowProgress(phase string, blocked []string, next string) string {
	phase = strings.TrimSpace(phase)
	if phase == "" {
		return ""
	}
	blocked = nonEmptyStrings(blocked)
	if strings.TrimSpace(next) == "" {
		next = defaultWorkflowNext
	}
	if len(blocked) == 0 {
		return fmt.Sprintf("phase=%s; next=%s", phase, strings.TrimSpace(next))
	}
	return fmt.Sprintf("phase=%s; blocked=%s; next=%s", phase, strings.Join(blocked, ","), strings.TrimSpace(next))
}

// ProgressDedupKey hashes workflow progress for per-session dedup.
func ProgressDedupKey(wf WorkflowEvaluationContext) string {
	if strings.TrimSpace(wf.WorkflowID) == "" && strings.TrimSpace(wf.CurrentPhase) == "" {
		return ""
	}
	parts := []string{
		strings.TrimSpace(wf.WorkflowID),
		strings.TrimSpace(wf.CurrentPhase),
		strings.Join(nonEmptyStrings(wf.FailedLeaves), ","),
	}
	return strings.Join(parts, "|")
}

func nonEmptyStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}
