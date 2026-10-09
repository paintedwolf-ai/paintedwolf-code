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
	Evidence []string       `json:"evidence,omitempty"`
	Question string         `json:"question,omitempty"`
	Scope    *api.TaskScope `json:"scope,omitempty"`
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
