package progress

import "strings"

const (
	// MaxAuthorProgressLines is the checklist row budget for update_progress.
	MaxAuthorProgressLines = 48
)

// ProgressTemplateVars returns pongo context for coordinator progress-checklist copy.
func ProgressTemplateVars() map[string]any {
	return map[string]any{
		"max_author_progress_lines": MaxAuthorProgressLines,
		"max_progress_label_chars":  MaxLabelRunes,
	}
}

// ValidateAuthorProgress runs structural update_progress guards before a checklist write lands.
func ValidateAuthorProgress(content string) (code string, data map[string]any, ok bool) {
	if n := PlanChecklistLineCount(content); n > MaxAuthorProgressLines {
		return "PROGRESS_TOO_MANY_LINES", map[string]any{
			"line_count": n,
			"max_lines":  MaxAuthorProgressLines,
		}, false
	}
	if line, found := FirstNestedChecklistLine(content); found {
		return "PROGRESS_NESTED_LINE", map[string]any{
			"line": PreviewLabel(line),
		}, false
	}
	if label, found := FirstPendingAfterOptional(content); found {
		return "PROGRESS_PENDING_AFTER_OPTIONAL", map[string]any{
			"label": PreviewLabel(label),
		}, false
	}
	// Long labels pass; parseChecklist clamps them for display.
	return "", nil, true
}

// PlanChecklistLineCount returns parsed checklist rows (optional `- [>]` included).
func PlanChecklistLineCount(content string) int {
	return len(parseChecklist(content))
}

// FirstNestedChecklistLine returns the first indented checklist line.
func FirstNestedChecklistLine(content string) (line string, found bool) {
	for _, raw := range strings.Split(content, "\n") {
		if raw == strings.TrimLeft(raw, " \t") {
			continue
		}
		trimmed := strings.TrimSpace(raw)
		if len(parseChecklistItemsInLine(trimmed)) > 0 {
			return trimmed, true
		}
	}
	return "", false
}

// FirstPendingAfterOptional returns the first `- [ ]` label after an optional `- [>]` line.
func FirstPendingAfterOptional(content string) (label string, found bool) {
	sawOptional := false
	for _, item := range ChecklistItems(content) {
		if item.State == ProgressStateOptional {
			sawOptional = true
			continue
		}
		if sawOptional && item.State == ProgressStatePending {
			return item.Label, true
		}
	}
	return "", false
}
