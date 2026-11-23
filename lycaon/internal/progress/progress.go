// Package progress manages root-session progress rows and their wire digest.
package progress

import (
	"strings"

	"github.com/lycaon/lycaon/internal/runeclamp"
	"github.com/lycaon/lycaon/pkg/api"
)

const (
	// DefaultProgressCap bounds how many steps a digest returns.
	DefaultProgressCap = 48
	// MaxProgressCap is the hard ceiling on returned steps.
	MaxProgressCap = 48
	// MaxAuthorProgressRows bounds total rows per session plan.
	MaxAuthorProgressRows = 48
	// MaxAuthorLabelRunes bounds coordinator-authored row labels.
	MaxAuthorLabelRunes = 240
	// MaxDetailedProgressUpdateRows bounds enumerated changes in transcript rows.
	MaxDetailedProgressUpdateRows = 12
	// MaxLabelRunes bounds each projected ProgressStep label.
	MaxLabelRunes = 60

	ProgressStatePending  = "pending"
	ProgressStateDone     = "done"
	ProgressStateNA       = "na"
	ProgressStateOptional = "optional"
)

// Row is one persisted progress deliverable keyed by short session-monotonic ID.
type Row struct {
	ID    string `json:"id"`
	State string `json:"state"`
	Label string `json:"label"`
}

// ProgressResult is the projected step list plus an overflow count.
type ProgressResult struct {
	Items []api.ProgressStep
	More  int
}

// ProgressTemplateVars returns pongo context for progress copy.
func ProgressTemplateVars() map[string]any {
	return map[string]any{
		"max_author_progress_rows": MaxAuthorProgressRows,
		"max_progress_label_chars": MaxLabelRunes,
	}
}

// SummarizeUpdate summarizes the plan after a progress_update changing changeCount rows.
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

// DeriveProgress projects non-optional rows into clamped ProgressStep items.
func DeriveProgress(rows []Row, limit int) ProgressResult {
	if limit <= 0 {
		limit = DefaultProgressCap
	}
	if limit > MaxProgressCap {
		limit = MaxProgressCap
	}
	items := VisibleSteps(rows)
	more := 0
	if len(items) > limit {
		more = len(items) - limit
		items = items[:limit]
	}
	return ProgressResult{Items: items, More: more}
}

// VisibleSteps drops optional rows and clamps labels for display.
func VisibleSteps(rows []Row) []api.ProgressStep {
	if len(rows) == 0 {
		return []api.ProgressStep{}
	}
	out := make([]api.ProgressStep, 0, len(rows))
	for _, r := range rows {
		if r.State == ProgressStateOptional {
			continue
		}
		out = append(out, api.ProgressStep{
			State: r.State,
			Label: clampLabel(r.Label),
		})
	}
	return out
}

// RenderMarkdown formats rows as checklist lines for human-readable summaries.
func RenderMarkdown(rows []Row) string {
	if len(rows) == 0 {
		return ""
	}
	var b strings.Builder
	for i, r := range rows {
		if i > 0 {
			b.WriteByte('\n')
		}
		switch r.State {
		case ProgressStateDone:
			b.WriteString("- [x] ")
		case ProgressStateNA:
			b.WriteString("- [~] ")
		case ProgressStateOptional:
			b.WriteString("- [>] ")
		default:
			b.WriteString("- [ ] ")
		}
		b.WriteString(strings.TrimSpace(r.Label))
	}
	return b.String()
}

// IsValidRowID reports whether id matches p[1-9][0-9]*.
func IsValidRowID(id string) bool {
	if len(id) < 2 || id[0] != 'p' || id[1] < '1' || id[1] > '9' {
		return false
	}
	for i := 2; i < len(id); i++ {
		if id[i] < '0' || id[i] > '9' {
			return false
		}
	}
	return true
}

func isValidState(state string) bool {
	switch state {
	case ProgressStatePending, ProgressStateDone, ProgressStateNA, ProgressStateOptional:
		return true
	default:
		return false
	}
}

func clampLabel(label string) string {
	return runeclamp.Clamp(strings.TrimSpace(label), MaxLabelRunes)
}

// PreviewLabel shortens a label for diagnostic rejection facts.
func PreviewLabel(label string) string {
	const previewRunes = 80
	return runeclamp.Clamp(strings.TrimSpace(label), previewRunes)
}
