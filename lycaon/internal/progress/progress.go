// Package progress holds the coordinator-authored, root-session-scoped checklist
// and its digest into ProgressStep rows.
package progress

import (
	"github.com/lycaon/lycaon/internal/runeclamp"
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

const (
	// DefaultProgressCap bounds how many steps a digest returns.
	DefaultProgressCap = 48
	// MaxProgressCap is the hard ceiling on returned steps.
	MaxProgressCap = 48
	// MaxDetailedProgressUpdateRows is the largest transcript update that
	// enumerates every changed row. Larger updates carry aggregate counts.
	MaxDetailedProgressUpdateRows = 12

	// MaxLabelRunes bounds each checklist label.
	MaxLabelRunes = 60

	ProgressStatePending = "pending"
	ProgressStateDone    = "done"
	// ProgressStateNA is terminal (`- [~]`) but not delivered work.
	ProgressStateNA = "na"
	// ProgressStateOptional is non-blocking (`- [>]`); DeriveProgress drops it.
	ProgressStateOptional = "optional"
)

// ProgressResult is the parsed checklist plus an overflow count.
type ProgressResult struct {
	Items []api.ProgressStep
	More  int
}

// SummarizeUpdate returns aggregate state for a progress_update whose changed
// rows are too numerous to render individually in the transcript.
func SummarizeUpdate(steps []api.ProgressStep, changeCount int) api.ProgressUpdateSummary {
	summary := api.ProgressUpdateSummary{
		ChangeCount: changeCount,
		TotalSteps:  len(steps),
	}
	for _, step := range steps {
		switch step.State {
		case ProgressStatePending:
			summary.Pending++
		case ProgressStateDone:
			summary.Done++
		case ProgressStateNA:
			summary.NA++
		}
	}
	return summary
}

// DeriveProgress parses the coordinator's markdown checklist into progress rows:
// `- [ ] x` → pending, `- [x] x` → done, `- [~] x` → n/a, `- [>] x` → optional (non-blocking).
// Non-checklist lines are ignored.
func DeriveProgress(content string, cap int) ProgressResult {
	if cap <= 0 {
		cap = DefaultProgressCap
	}
	if cap > MaxProgressCap {
		cap = MaxProgressCap
	}
	items := visibleChecklist(content)
	more := 0
	if len(items) > cap {
		more = len(items) - cap
		items = items[:cap]
	}
	return ProgressResult{Items: items, More: more}
}

// visibleChecklist parses the checklist and drops optional (`- [>]`) lines.
func visibleChecklist(content string) []api.ProgressStep {
	parsed := parseChecklist(content)
	out := make([]api.ProgressStep, 0, len(parsed))
	for _, it := range parsed {
		if it.State == ProgressStateOptional {
			continue
		}
		out = append(out, it)
	}
	return out
}

// ProgressMissing reports whether the coordinator has not yet authored any checklist step.
func ProgressMissing(content string) bool {
	return len(parseChecklist(content)) == 0
}

// CloseCounts tallies a checklist by state. The closure guard compares the closed count
// (done + n/a — both terminal) against a snapshot to detect a coordinator doing work without
// closing the steps it finishes.
func CloseCounts(content string) (done, pending, na int) {
	for _, item := range parseChecklist(content) {
		switch item.State {
		case ProgressStateDone:
			done++
		case ProgressStatePending:
			pending++
		case ProgressStateNA:
			na++
		}
	}
	return done, pending, na
}

// HasOpenSteps reports whether the plan has blocking `- [ ]` rows. Missing plans and
// all-terminal checklists (including optional `- [>]` only leftovers) have none.
func HasOpenSteps(content string) bool {
	return OpenStepCount(content) > 0
}

// OpenStepCount returns the remaining blocking `- [ ]` rows.
func OpenStepCount(content string) int {
	_, pending, _ := CloseCounts(content)
	return pending
}

// AllTerminal reports whether a checklist has at least one step and none pending.
// Done, n/a, and optional all count as terminal.
func AllTerminal(content string) bool {
	items := parseChecklist(content)
	if len(items) == 0 {
		return false
	}
	for _, item := range items {
		if item.State == ProgressStatePending {
			return false
		}
	}
	return true
}

func parseChecklist(content string) []api.ProgressStep {
	items := ChecklistItems(content)
	for i := range items {
		items[i].Label = clampLabel(items[i].Label)
	}
	return items
}

// ChecklistItems returns raw checklist rows (uncapped labels) for obligation scanning.
func ChecklistItems(content string) []api.ProgressStep {
	var items []api.ProgressStep
	for _, raw := range strings.Split(content, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		items = append(items, parseChecklistItemsInLine(line)...)
	}
	return items
}

func parseChecklistItemsInLine(line string) []api.ProgressStep {
	var items []api.ProgressStep
	search := 0
	for {
		idx, ok := findNextChecklistMarker(line, search)
		if !ok {
			break
		}
		state, label, end, ok := parseChecklistItemAt(line, idx)
		if !ok {
			search = idx + 1
			continue
		}
		items = append(items, api.ProgressStep{State: state, Label: label})
		if end <= idx {
			search = idx + 1
		} else {
			search = end
		}
	}
	return items
}

func clampLabel(label string) string {
	return runeclamp.Clamp(label, MaxLabelRunes)
}

// PreviewLabel shortens a label for reject copy without blowing the guidance budget.
func PreviewLabel(label string) string {
	const previewRunes = 80
	return runeclamp.Clamp(strings.TrimSpace(label), previewRunes)
}

func findNextChecklistMarker(line string, start int) (int, bool) {
	for i := start; i < len(line); i++ {
		if line[i] != '-' && line[i] != '*' {
			continue
		}
		if i+2 >= len(line) || line[i+1] != ' ' || line[i+2] != '[' {
			continue
		}
		return i, true
	}
	return 0, false
}

func parseChecklistItemAt(line string, start int) (state, label string, end int, ok bool) {
	if start >= len(line) || (line[start] != '-' && line[start] != '*') {
		return "", "", start, false
	}
	i := start + 1
	if i < len(line) && line[i] == ' ' {
		i++
	}
	if i+3 > len(line) || line[i] != '[' || line[i+2] != ']' {
		return "", "", start, false
	}
	state, ok = checklistMarkerState(line[i+1])
	if !ok {
		return "", "", start, false
	}
	j := i + 3
	if j < len(line) && line[j] == ' ' {
		j++
	}
	labelEnd := len(line)
	if next, found := findNextChecklistMarker(line, j); found {
		labelEnd = next
	}
	label = strings.TrimSpace(line[j:labelEnd])
	if label == "" {
		return "", "", start, false
	}
	return state, label, labelEnd, true
}

func checklistMarkerState(marker byte) (state string, ok bool) {
	switch marker {
	case ' ':
		return ProgressStatePending, true
	case 'x', 'X':
		return ProgressStateDone, true
	case '~', '-':
		return ProgressStateNA, true
	case '>':
		return ProgressStateOptional, true
	default:
		return "", false
	}
}
