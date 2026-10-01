package feedback

import (
	"strings"

	"github.com/lycaon/lycaon/internal/conditions"
)

// WithReviewAgents stamps the verdict-owed reviewer roster into gate-feedback
// extras as `review_agents` (backtick-quoted, comma-joined) so evidence-gate
// satisfy copy can name the reviewers a terminal verdict requires.
func WithReviewAgents(extras map[string]any, agents []string) map[string]any {
	if extras == nil {
		extras = map[string]any{}
	}
	parts := make([]string, 0, len(agents))
	for _, a := range agents {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		parts = append(parts, "`"+a+"`")
	}
	if len(parts) > 0 {
		extras["review_agents"] = strings.Join(parts, ", ")
	}
	return extras
}

// PlanStubGateExtras builds gate-feedback context extras for plan_stub_valid diagnostics.
// Empty content yields stub_sections_missing=true with the full required list.
func PlanStubGateExtras(planContent string) map[string]any {
	// The full required set is headings plus structured frontmatter fields; the
	// agent has to write both to satisfy the gate.
	required := conditions.PlanStubRequiredLabels()
	missing := conditions.PlanStubMissingRequired(planContent)
	present := conditions.MarkdownH2Headings(planContent)
	return map[string]any{
		"stub_sections_missing":   len(missing) > 0,
		"missing_stub_required":   missing,
		"present_h2":              present,
		"missing_stub_required_s": strings.Join(missing, ", "),
		"present_h2_s":            strings.Join(present, ", "),
		"required_stub_labels":    required,
		"required_stub_labels_s":  strings.Join(required, ", "),
	}
}
