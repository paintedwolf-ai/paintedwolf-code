package reviewcoverage

import (
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestAssignmentRevisionTracksSubjectNotReviewerLifecycle(t *testing.T) {
	facts := Facts{Obligations: []Fact{{ID: "area", Kind: "planned_area", Tasks: []string{"survey"}}}, Gaps: []Fact{{ID: "gap/full-scope", Kind: "file_partial_semantics", FileCount: 654}}}
	candidate := api.CoverageReview{Revision: "candidate", Assessments: []api.CoverageAssessment{{ID: "gap/full-scope", Disposition: Immaterial, Reason: "Excluded by candidate"}}}
	first := Assign(facts, candidate, "check")
	facts.Revision = "new lifecycle revision"
	facts.Gaps = append(facts.Gaps, Fact{ID: "reviewer", Kind: "supporting_work", Phase: "check"}, Fact{ID: "question/1", Kind: "review_question"})
	if got := Assign(facts, candidate, "check"); got.Facts.Revision != first.Facts.Revision || len(got.Facts.Gaps) != 1 {
		t.Fatal("reviewer or question lifecycle invalidated scope assessment")
	}
	prior := facts
	prior.Gaps = append(append([]Fact(nil), facts.Gaps...), Fact{ID: "unsettled-survey", Kind: "supporting_work", Phase: "survey"})
	if Assign(prior, candidate, "check").Facts.Revision == first.Facts.Revision {
		t.Fatal("prior supporting work escaped independent review")
	}
	facts.Gaps[0].ID = "gap/changed-full-scope"
	if Assign(facts, candidate, "check").Facts.Revision == first.Facts.Revision {
		t.Fatal("changed scope retained assessment")
	}
	facts.Gaps[0].ID = "gap/full-scope"
	candidate.Assessments[0].Reason = "Changed candidate judgment"
	if Assign(facts, candidate, "check").Facts.Revision == first.Facts.Revision {
		t.Fatal("changed candidate retained assessment")
	}
}
