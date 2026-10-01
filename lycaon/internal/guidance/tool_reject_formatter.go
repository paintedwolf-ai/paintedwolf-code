package guidance

import (
	"context"
	"strings"
)

// Phase-gate rejections include checklist state.
type ToolRejectFormatter struct {
	dedup *FeedbackDeduper
}

func NewToolRejectFormatter(dedup *FeedbackDeduper) *ToolRejectFormatter {
	if dedup == nil {
		dedup = NewFeedbackDeduper()
	}
	return &ToolRejectFormatter{dedup: dedup}
}

// Session removal releases checklist deduplication state.
func (f *ToolRejectFormatter) ForgetSession(sessionID string) {
	if f == nil || f.dedup == nil {
		return
	}
	f.dedup.ClearSession(sessionID)
}

// FormatBlock renders the compact block, optionally with Details: appendix.
func (f *ToolRejectFormatter) FormatBlock(ctx context.Context, sessionID string, attemptedTool string, out any, progress PlanProgress, copy map[string]string) (string, error) {
	return RenderSpecPostureRejectBlock(ctx, sessionID, attemptedTool, out, progress, copy, f)
}

func (f *ToolRejectFormatter) shouldAppendDetails(sessionID, checklistHash string) bool {
	if f == nil || f.dedup == nil {
		return true
	}
	return f.dedup.ShouldAppendDetails(sessionID, checklistHash)
}

func formatProgressCompact(progress PlanProgress, phaseRequired string) string {
	checklist := strings.TrimSpace(progress.ProgressChecklist)
	if checklist == "" {
		if progress.NextAction != "" {
			return progress.NextAction
		}
		return ""
	}
	if strings.TrimSpace(phaseRequired) == "" {
		lines := strings.Split(checklist, "\n")
		if len(lines) > 0 {
			return strings.TrimSpace(lines[0])
		}
		return checklist
	}
	want := "Phase " + strings.TrimSpace(phaseRequired) + " —"
	for _, line := range strings.Split(checklist, "\n") {
		line = strings.TrimSpace(line)
		if strings.Contains(line, want) {
			return line
		}
	}
	return progress.NextAction
}
