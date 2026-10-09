package reviewcoverage

import (
	"fmt"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

// Issue identifies a repair without asking callers to parse diagnostic prose.
type Issue struct {
	Kind       string   `json:"kind"`
	FieldPath  string   `json:"field_path"`
	FactID     string   `json:"fact_id,omitempty"`
	Expected   []string `json:"expected,omitempty"`
	Actual     string   `json:"actual,omitempty"`
	Repairable bool     `json:"repairable"`
	Message    string   `json:"message"`
}

type ValidationError struct{ Issues []Issue }

func (e *ValidationError) Error() string {
	messages := make([]string, len(e.Issues))
	for i, issue := range e.Issues {
		messages[i] = issue.Message
	}
	return strings.Join(messages, "; ")
}

// Validate collects independent defects against the complete current fact set.
func Validate(f Facts, r api.CoverageReview) error {
	var issues []Issue
	add := func(kind, path, id, actual, message string, expected ...string) {
		issues = append(issues, Issue{kind, path, id, expected, actual, true, message})
	}
	if r.Revision != f.Revision || r.Revision == "" {
		add("stale_revision", "revision", "", r.Revision, "coverage revision is stale; assess the current coverage facts", f.Revision)
		return &ValidationError{issues}
	}
	known := map[string]Fact{}
	obligations := map[string]bool{}
	for _, fact := range f.Obligations {
		known[fact.ID] = fact
		obligations[fact.ID] = true
	}
	for _, fact := range f.Gaps {
		known[fact.ID] = fact
	}
	seen := map[string]bool{}
	for i, a := range r.Assessments {
		path := fmt.Sprintf("assessments[%d]", i)
		fact, exists := known[a.ID]
		if !exists {
			add("unknown_fact", path+".id", a.ID, a.ID, fmt.Sprintf("coverage assessment %q names no current fact; current ids: %s", a.ID, f.idSample()))
			continue
		}
		if seen[a.ID] {
			add("duplicate_fact", path+".id", a.ID, a.ID, fmt.Sprintf("coverage assessment %q is repeated; assess each fact once", a.ID))
			continue
		}
		seen[a.ID] = true
		accepted := acceptedDispositions(obligations[a.ID])
		if fact.Blocking {
			accepted = []string{EssentialOpen}
		}
		if !slices.Contains(accepted, a.Disposition) {
			add("disposition", path+".disposition", a.ID, a.Disposition, fmt.Sprintf("coverage assessment %q is %s; disposition must be one of %s (got %q)", a.ID, factClass(obligations[a.ID]), strings.Join(accepted, ", "), a.Disposition), accepted...)
		}
		if strings.TrimSpace(a.Reason) == "" {
			add("missing_reason", path+".reason", a.ID, "", fmt.Sprintf("coverage assessment %q needs a reason", a.ID))
		}
		if len(a.CitedEvidence) == 0 {
			add("missing_evidence", path+".cited_evidence", a.ID, "", fmt.Sprintf("coverage assessment %q requires cited evidence", a.ID))
		}
		for j, c := range a.CitedEvidence {
			if (c.Handle == "" && c.Path == "") || c.Line < 0 || (c.Path == "" && (c.Line != 0 || c.Excerpt != "")) {
				add("invalid_citation", fmt.Sprintf("%s.cited_evidence[%d]", path, j), a.ID, "", fmt.Sprintf("coverage assessment %q has an invalid citation; cite an observed evidence handle, a path with an optional line and excerpt, or both naming one observation", a.ID))
			}
		}
		if !obligations[a.ID] && len(a.Obligations) == 0 {
			add("missing_obligations", path+".obligations", a.ID, "", fmt.Sprintf("coverage gap %q must name the obligations it affects; current obligations: %s", a.ID, idList(f.Obligations)), factIDs(f.Obligations)...)
		}
		for _, id := range a.Obligations {
			if !obligations[id] {
				add("unknown_obligation", path+".obligations", a.ID, id, fmt.Sprintf("coverage assessment %q names unknown obligation %q; current obligations: %s", a.ID, id, idList(f.Obligations)))
			}
		}
	}
	for _, fact := range append(append([]Fact(nil), f.Obligations...), f.Gaps...) {
		if !seen[fact.ID] {
			add("missing_assessment", "assessments", fact.ID, "", "unassessed: "+fact.ID)
		}
	}
	if err := validateQuestionObligations(f, r); err != nil {
		add("question_obligations", "assessments", "", "", err.Error())
	}
	if len(issues) > 0 {
		return &ValidationError{issues}
	}
	return nil
}
