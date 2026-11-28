package hoststate

import (
	"fmt"
	"slices"

	"github.com/lycaon/lycaon/internal/pongoplain"
)

// ExpectUnrendered records the production constraint that keeps every
// recipient state the matrix models from rendering unitID.
func (t *CoverageTracker) ExpectUnrendered(unitID, reason string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.expectUnrendered == nil {
		t.expectUnrendered = map[string]ExpectedZeroBranch{}
	}
	t.expectUnrendered[unitID] = ExpectedZeroBranch{ID: unitID, Reason: reason}
}

// renderCoverage splits the registered templates into those some case
// executed, root or included, and those none did.
func (t *CoverageTracker) renderCoverage(ids []string, specs map[string][]pongoplain.BranchSpec, resolve func(string) (string, bool)) (int, []string, []ExpectedZeroBranch) {
	t.mu.Lock()
	expected := make(map[string]ExpectedZeroBranch, len(t.expectUnrendered))
	for k, v := range t.expectUnrendered {
		expected[k] = v
	}
	t.mu.Unlock()

	reached := map[string]bool{}
	t.templateRec.Mu.Lock()
	for name := range t.templateRec.Rendered {
		if unit, ok := attributeTemplate(name, specs, resolve); ok {
			reached[unit] = true
		}
	}
	t.templateRec.Mu.Unlock()

	rendered := 0
	var unrendered []string
	var excused []ExpectedZeroBranch
	for _, id := range ids {
		switch ex, ok := expected[id]; {
		case reached[id] && ok:
			unrendered = append(unrendered, id+" (rendered although "+ex.Reason+")")
		case reached[id]:
			rendered++
		case ok:
			excused = append(excused, ex)
		default:
			unrendered = append(unrendered, id)
		}
	}
	return rendered, unrendered, excused
}

// corpusIssues refuses a report that judged nothing: no registered template,
// rule, or observed branch of either kind, and lists templates no case
// rendered.
func (r CoverageReport) corpusIssues() []string {
	var issues []string
	if len(r.DiscoveredTemplates) == 0 {
		issues = append(issues, "no stock template is registered")
	}
	if len(r.DiscoveredRules) == 0 {
		issues = append(issues, "no OAR rule is registered")
	}
	if r.RenderedTemplates == 0 {
		issues = append(issues, "no registered template was rendered")
	}
	if !slices.ContainsFunc(r.TemplateBranches, func(b TemplateBranchSummary) bool { return !b.Dead() }) {
		issues = append(issues, "no template branch was evaluated")
	}
	if !slices.ContainsFunc(r.RuleBranches, func(b RuleBranchSummary) bool { return !b.Dead() }) {
		issues = append(issues, "no OAR branch was evaluated")
	}
	for _, id := range r.UnrenderedTemplates {
		issues = append(issues, fmt.Sprintf("no recipient state renders template %s", id))
	}
	return issues
}
