// Package reviewcoverage binds review judgments to the exact work they assess.
package reviewcoverage

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

// Fact is a host observation bound to the review revision.
type Fact struct {
	InvestigationAttempts int            `json:"investigation_attempts,omitempty"`
	FollowupLimit         int            `json:"followup_limit,omitempty"`
	InvestigationActive   bool           `json:"investigation_active,omitempty"`
	ReviewRequired        bool           `json:"review_required,omitempty"`
	Obligations           []string       `json:"obligations,omitempty"`
	Blocking              bool           `json:"blocking,omitempty"`
	ID                    string         `json:"id"`
	Kind                  string         `json:"kind"`
	Subject               string         `json:"subject"`
	FileCount             int            `json:"file_count,omitempty"`
	Count                 int            `json:"count,omitempty"`
	Paths                 []string       `json:"paths,omitempty"`
	Scans                 []string       `json:"scans,omitempty"`
	Tasks                 []string       `json:"tasks,omitempty"`
	Question              string         `json:"question,omitempty"`
	Scope                 *api.TaskScope `json:"scope,omitempty"`
}

// Facts identifies the complete input to a coverage review.
type Facts struct {
	Revision    string `json:"revision"`
	Obligations []Fact `json:"obligations"`
	Gaps        []Fact `json:"gaps"`
}

// Assessment states whether an obligation or observed limitation leaves work open.
type Assessment struct {
	ID            string                               `json:"id"`
	Disposition   string                               `json:"disposition"`
	Reason        string                               `json:"reason"`
	Obligations   []string                             `json:"obligations,omitempty"`
	CitedEvidence []api.CitationGroundingCitedEvidence `json:"cited_evidence"`
}

// Review is persisted inside the declaring workflow's verdict evidence.
type Review struct {
	Revision    string       `json:"revision"`
	Assessments []Assessment `json:"assessments"`
}

const (
	Satisfied     = "satisfied"
	Covered       = "covered"
	Immaterial    = "immaterial"
	MaterialOpen  = "material_open"
	EssentialOpen = "essential_open"
)

// Identity is deterministic over the full fact, before presentation is bounded.
func Identity(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(fmt.Sprintf("invalid coverage evidence: %v", err))
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// Seal establishes a stable revision independent of observation order.
func (f *Facts) Seal() {
	for _, rows := range [][]Fact{f.Obligations, f.Gaps} {
		slices.SortFunc(rows, func(a, b Fact) int { return strings.Compare(a.ID, b.ID) })
	}
	f.Revision = Identity(struct{ Obligations, Gaps []Fact }{f.Obligations, f.Gaps})
}

// Validate requires an explicit assessment of every current fact. Evidence is
// resolved by the workflow's grounding audit after this structural check.
func Validate(f Facts, r Review) error {
	if r.Revision != f.Revision || r.Revision == "" {
		return fmt.Errorf("coverage revision is stale; assess the current coverage facts")
	}
	known := map[string]bool{}
	obligations := map[string]bool{}
	blocking := map[string]bool{}
	for _, fact := range f.Obligations {
		known[fact.ID] = true
		blocking[fact.ID] = fact.Blocking
		obligations[fact.ID] = true
	}
	for _, fact := range f.Gaps {
		known[fact.ID] = true
		blocking[fact.ID] = fact.Blocking
	}
	seen := map[string]bool{}
	for _, a := range r.Assessments {
		if !known[a.ID] || seen[a.ID] {
			return fmt.Errorf("coverage assessment %q is unknown or repeated", a.ID)
		}
		seen[a.ID] = true
		if blocking[a.ID] && a.Disposition != EssentialOpen {
			return fmt.Errorf("coverage fact %q records essential work that has not completed", a.ID)
		}
		valid := a.Disposition == MaterialOpen || a.Disposition == EssentialOpen
		if obligations[a.ID] {
			valid = valid || a.Disposition == Satisfied
		} else {
			valid = valid || a.Disposition == Covered || a.Disposition == Immaterial
		}
		if !valid || strings.TrimSpace(a.Reason) == "" {
			return fmt.Errorf("coverage assessment %q requires a valid disposition and reason", a.ID)
		}
		if len(a.CitedEvidence) == 0 {
			return fmt.Errorf("coverage assessment %q requires cited evidence", a.ID)
		}
		for _, c := range a.CitedEvidence {
			if (c.Handle == "") == (c.Path == "") || c.Line < 0 || (c.Path == "" && (c.Line != 0 || c.Excerpt != "")) {
				return fmt.Errorf("coverage assessment %q has an invalid citation", a.ID)
			}
		}
		if !obligations[a.ID] && len(a.Obligations) == 0 {
			return fmt.Errorf("coverage gap %q must name the obligations it affects", a.ID)
		}
		for _, id := range a.Obligations {
			if !obligations[id] {
				return fmt.Errorf("coverage assessment %q names unknown obligation %q", a.ID, id)
			}
		}
	}
	if len(seen) != len(known) {
		return fmt.Errorf("coverage assesses %d of %d current obligations and gaps", len(seen), len(known))
	}
	return validateQuestionObligations(f, r)
}

func validateQuestionObligations(f Facts, r Review) error {
	assessments := map[string]Assessment{}
	for _, a := range r.Assessments {
		assessments[a.ID] = a
	}
	for _, fact := range f.Gaps {
		if fact.Kind != "review_question" {
			continue
		}
		a := assessments[fact.ID]
		if len(a.Obligations) != len(fact.Obligations) {
			return fmt.Errorf("question %s must retain its affected obligations", fact.ID)
		}
		for _, id := range fact.Obligations {
			if !slices.Contains(a.Obligations, id) {
				return fmt.Errorf("question %s must retain obligation %s", fact.ID, id)
			}
			parent := assessments[id]
			if a.Disposition == EssentialOpen && parent.Disposition != EssentialOpen || a.Disposition == MaterialOpen && parent.Disposition == Satisfied {
				return fmt.Errorf("obligation %s conflicts with open question %s", id, fact.ID)
			}
		}
	}
	return nil
}

// Completeness reflects reviewed work; callers retain independent hard failures.
func (r Review) Completeness() string {
	level := "complete"
	for _, a := range r.Assessments {
		switch a.Disposition {
		case EssentialOpen:
			return "incomplete"
		case MaterialOpen:
			level = "mostly"
		}
	}
	return level
}
