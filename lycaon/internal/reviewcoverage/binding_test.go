package reviewcoverage

import (
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestReviewOutputDoesNotChangeAssignedInputs(t *testing.T) {
	facts := Facts{Obligations: []Fact{{ID: "area", Kind: "planned_area"}}}
	first := Assign(facts, api.CoverageReview{}, "challenge")
	facts.Gaps = append(facts.Gaps, Fact{ID: "review/new-gap", Kind: "worker_scope", Phase: "challenge", Obligations: []string{"area"}})
	next := Assign(facts, api.CoverageReview{}, "challenge")
	if first.Facts.Revision != next.Facts.Revision || len(ChangedItems(first, next)) != 0 {
		t.Fatal("review output invalidated its own subject")
	}
	facts.Obligations[0].Question = "A changed planned question"
	next = Assign(facts, api.CoverageReview{}, "challenge")
	if changed := ChangedItems(first, next); len(changed) != 1 || changed[0] != "area" {
		t.Fatalf("changed scope escaped dependency check: %v", changed)
	}
}

func TestScopedReassessmentPreservesUnrelatedJudgments(t *testing.T) {
	evidence := []api.CitationGroundingCitedEvidence{{Handle: "read#1"}}
	facts := Facts{Obligations: []Fact{{ID: "a"}, {ID: "b"}}}
	candidate := api.CoverageReview{Assessments: []api.CoverageAssessment{{ID: "a", Disposition: Satisfied, Reason: "a observed", CitedEvidence: evidence}, {ID: "b", Disposition: Satisfied, Reason: "b observed", CitedEvidence: evidence}}}
	subject := Assign(facts, candidate, "challenge")
	scoped := Scoped(subject, []string{"a"})
	update := api.CoverageReview{Revision: scoped.Facts.Revision, Assessments: []api.CoverageAssessment{{ID: "a", Disposition: MaterialOpen, Reason: "new evidence", CitedEvidence: evidence}}}
	composed, err := Compose(candidate, update, scoped.Facts)
	if err != nil {
		t.Fatalf("compose reassessment: %v", err)
	}
	if composed.Assessments[0].Disposition != MaterialOpen || composed.Assessments[1].Reason != "b observed" || candidate.Assessments[0].Disposition != Satisfied {
		t.Fatal("scoped update damaged unrelated or historical judgment")
	}
	update.Assessments = append(update.Assessments, candidate.Assessments[1])
	if _, err := Compose(candidate, update, scoped.Facts); err == nil {
		t.Fatal("accepted undeclared scope expansion")
	}
}
