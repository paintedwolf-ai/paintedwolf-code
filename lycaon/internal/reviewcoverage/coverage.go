// Package reviewcoverage binds review judgments to the exact work they assess.
package reviewcoverage

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	scancoverage "github.com/lycaon/lycaon/internal/scan/coverage"
	"github.com/lycaon/lycaon/pkg/api"
)

// Fact is a host observation bound to the review revision.
type Fact struct {
	Phase                 string                `json:"phase,omitempty"`
	Distribution          *scancoverage.Profile `json:"distribution,omitempty"`
	InvestigationAttempts int                   `json:"investigation_attempts,omitempty"`
	FollowupLimit         int                   `json:"followup_limit,omitempty"`
	InvestigationActive   bool                  `json:"investigation_active,omitempty"`
	ReviewRequired        bool                  `json:"review_required,omitempty"`
	Obligations           []string              `json:"obligations,omitempty"`
	Blocking              bool                  `json:"blocking,omitempty"`
	ID                    string                `json:"id"`
	Kind                  string                `json:"kind"`
	Subject               string                `json:"subject"`
	FileCount             int                   `json:"file_count,omitempty"`
	Count                 int                   `json:"count,omitempty"`
	Paths                 []string              `json:"paths,omitempty"`
	Scans                 []string              `json:"scans,omitempty"`
	Tasks                 []string              `json:"tasks,omitempty"`
	// Evidence lists session-qualified handles the fact's legs cited.
	Evidence              []string              `json:"evidence,omitempty"`
	Question              string                `json:"question,omitempty"`
	Scope                 *api.TaskScope        `json:"scope,omitempty"`
}

// Facts identifies the complete input to a coverage review.
type Facts struct {
	Revision    string `json:"revision"`
	Obligations []Fact `json:"obligations"`
	Gaps        []Fact `json:"gaps"`
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
// Each refusal names the fact and what its class accepts.
func Validate(f Facts, r api.CoverageReview) error {
	if r.Revision != f.Revision || r.Revision == "" {
		return fmt.Errorf("coverage revision is stale; assess the current coverage facts")
	}
	known := map[string]bool{}
	obligations := map[string]bool{}
	blocking := map[string]bool{}
	for _, fact := range f.Obligations {
		known[fact.ID] = true
		obligations[fact.ID] = true
		blocking[fact.ID] = fact.Blocking
	}
	for _, fact := range f.Gaps {
		known[fact.ID] = true
		blocking[fact.ID] = fact.Blocking
	}
	seen := map[string]bool{}
	for _, a := range r.Assessments {
		if !known[a.ID] {
			return fmt.Errorf("coverage assessment %q names no current fact; current ids: %s", a.ID, f.idSample())
		}
		if seen[a.ID] {
			return fmt.Errorf("coverage assessment %q is repeated; assess each fact once", a.ID)
		}
		seen[a.ID] = true
		if blocking[a.ID] && a.Disposition != EssentialOpen {
			return fmt.Errorf("coverage fact %q records essential work that has not completed; its disposition must be %s", a.ID, EssentialOpen)
		}
		if accepted := acceptedDispositions(obligations[a.ID]); !slices.Contains(accepted, a.Disposition) {
			return fmt.Errorf("coverage assessment %q is %s; disposition must be one of %s (got %q)",
				a.ID, factClass(obligations[a.ID]), strings.Join(accepted, ", "), a.Disposition)
		}
		if strings.TrimSpace(a.Reason) == "" {
			return fmt.Errorf("coverage assessment %q needs a reason", a.ID)
		}
		if len(a.CitedEvidence) == 0 {
			return fmt.Errorf("coverage assessment %q requires cited evidence", a.ID)
		}
		for _, c := range a.CitedEvidence {
			if (c.Handle == "" && c.Path == "") || c.Line < 0 || (c.Path == "" && (c.Line != 0 || c.Excerpt != "")) {
				return fmt.Errorf("coverage assessment %q has an invalid citation; cite an observed evidence handle, a path with an optional line and excerpt, or both naming one observation", a.ID)
			}
		}
		if !obligations[a.ID] && len(a.Obligations) == 0 {
			return fmt.Errorf("coverage gap %q must name the obligations it affects; current obligations: %s", a.ID, idList(f.Obligations))
		}
		for _, id := range a.Obligations {
			if !obligations[id] {
				return fmt.Errorf("coverage assessment %q names unknown obligation %q; current obligations: %s", a.ID, id, idList(f.Obligations))
			}
		}
	}
	if len(seen) != len(known) {
		return fmt.Errorf("coverage assesses %d of %d current obligations and gaps; unassessed: %s", len(seen), len(known), f.unassessed(seen))
	}
	return validateQuestionObligations(f, r)
}

// acceptedDispositions: an obligation is satisfied or left open; a gap is
// covered by other evidence, immaterial, or left open.
func acceptedDispositions(obligation bool) []string {
	if obligation {
		return []string{Satisfied, MaterialOpen, EssentialOpen}
	}
	return []string{Covered, Immaterial, MaterialOpen, EssentialOpen}
}

func factClass(obligation bool) string {
	if obligation {
		return "an obligation"
	}
	return "a gap"
}

// idSampleLimit bounds the ids one refusal lists.
const idSampleLimit = 8

func idList(facts []Fact) string {
	return boundedList(factIDs(facts))
}

func factIDs(facts []Fact) []string {
	ids := make([]string, 0, len(facts))
	for _, fact := range facts {
		ids = append(ids, fact.ID)
	}
	return ids
}

func (f Facts) idSample() string {
	return boundedList(append(factIDs(f.Obligations), factIDs(f.Gaps)...))
}

func (f Facts) unassessed(seen map[string]bool) string {
	var ids []string
	for _, fact := range append(append([]Fact(nil), f.Obligations...), f.Gaps...) {
		if !seen[fact.ID] {
			ids = append(ids, fact.ID)
		}
	}
	return boundedList(ids)
}

func boundedList(ids []string) string {
	if len(ids) == 0 {
		return "(none)"
	}
	if len(ids) <= idSampleLimit {
		return strings.Join(ids, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(ids[:idSampleLimit], ", "), len(ids)-idSampleLimit)
}

func validateQuestionObligations(f Facts, r api.CoverageReview) error {
	assessments := map[string]api.CoverageAssessment{}
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
func Completeness(r api.CoverageReview) string {
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
