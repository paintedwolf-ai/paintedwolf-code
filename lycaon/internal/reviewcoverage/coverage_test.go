package reviewcoverage

import (
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestReviewRequiresCurrentEvidenceForEveryObligationAndGap(t *testing.T) {
	facts := Facts{Obligations: []Fact{{ID: "auth", Kind: "planned_area"}}, Gaps: []Fact{{ID: "parse", Kind: "partial_parse"}}}
	facts.Seal()
	cite := []api.CitationGroundingCitedEvidence{{Handle: "file#1"}}
	review := api.CoverageReview{Revision: facts.Revision, Assessments: []api.CoverageAssessment{
		{ID: "auth", Disposition: Satisfied, Reason: "Boundary independently traced", CitedEvidence: cite},
		{ID: "parse", Disposition: Covered, Reason: "Manual trace covers parser limitation", Obligations: []string{"auth"}, CitedEvidence: cite},
	}}
	if err := Validate(facts, review); err != nil {
		t.Fatalf("coverage fixture: %v", err)
	}
	if Completeness(review) != "complete" {
		t.Fatal("covered scanner gap degraded a satisfied review")
	}
	for _, disposition := range []string{MaterialOpen, EssentialOpen} {
		review.Assessments[1].Disposition = disposition
		want := "mostly"
		if disposition == EssentialOpen {
			want = "incomplete"
		}
		if Completeness(review) != want {
			t.Fatalf("%s must produce %s", disposition, want)
		}
	}
	review.Assessments[1].Disposition = Immaterial
	if err := Validate(facts, review); err != nil {
		t.Fatalf("coverage fixture: %v", err)
	}
	tests := map[string]func(*api.CoverageReview){
		"stale":                   func(r *api.CoverageReview) { r.Revision = "old" },
		"missing":                 func(r *api.CoverageReview) { r.Assessments = r.Assessments[:1] },
		"unknown":                 func(r *api.CoverageReview) { r.Assessments[1].ID = "unknown" },
		"duplicate":               func(r *api.CoverageReview) { r.Assessments[1] = r.Assessments[0] },
		"unsupported disposition": func(r *api.CoverageReview) { r.Assessments[1].Disposition = "ignored" },
		"unanchored":              func(r *api.CoverageReview) { r.Assessments[1].CitedEvidence = nil },
		"unmapped":                func(r *api.CoverageReview) { r.Assessments[1].Obligations = nil },
		"invented obligation":     func(r *api.CoverageReview) { r.Assessments[1].Obligations = []string{"other"} },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			r := review
			r.Assessments = append([]api.CoverageAssessment(nil), review.Assessments...)
			mutate(&r)
			if Validate(facts, r) == nil {
				t.Fatal("invalid assessment accepted")
			}
		})
	}
}

func TestReviewCannotWaiveUnfinishedEssentialWork(t *testing.T) {
	facts := Facts{Obligations: []Fact{{ID: "required", Blocking: true}}}
	facts.Seal()
	review := api.CoverageReview{Revision: facts.Revision, Assessments: []api.CoverageAssessment{{ID: "required", Disposition: Satisfied, Reason: "No issue found", CitedEvidence: []api.CitationGroundingCitedEvidence{{Handle: "scan#1"}}}}}
	if Validate(facts, review) == nil {
		t.Fatal("unfinished required work was waived")
	}
	review.Assessments[0].Disposition = EssentialOpen
	if err := Validate(facts, review); err != nil {
		t.Fatalf("honest incomplete assessment refused: %v", err)
	}
}
