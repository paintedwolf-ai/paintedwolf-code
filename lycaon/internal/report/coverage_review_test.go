package report

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/reviewcoverage"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestReviewedCoverageKeepsHardFailuresAndDisclosesLimits(t *testing.T) {
	facts := reviewcoverage.Facts{Obligations: []reviewcoverage.Fact{{ID: "boundary", Subject: "API boundary"}}, Gaps: []reviewcoverage.Fact{{ID: "parser", Subject: "SAST parser", FileCount: 1, Paths: []string{"fixture.go"}}}}
	facts.Seal()
	cite := []api.CitationGroundingCitedEvidence{{Handle: "file#1"}}
	review := api.CoverageReview{Revision: facts.Revision, Assessments: []api.CoverageAssessment{
		{ID: "boundary", Disposition: reviewcoverage.Satisfied, Reason: "Boundary traced", CitedEvidence: cite},
		{ID: "parser", Disposition: reviewcoverage.Immaterial, Reason: "Fixture is outside the shipped path", Obligations: []string{"boundary"}, CitedEvidence: cite},
	}}
	input := ReportInput{CoverageFacts: &facts, CoverageReview: &review, Gaps: []ReportGap{{Kind: GapScansMoved, Count: 3, Detail: 1}}}
	if input.Completeness() != CompletenessComplete {
		t.Fatal("reviewed immaterial gap degraded review")
	}
	if len(notCoveredItems(input)) != 0 {
		t.Fatal("assessed gap shown as unfinished work")
	}
	if !strings.Contains(strings.Join(coverageAssessmentItems(input), " "), "Fixture is outside the shipped path") {
		t.Fatal("assessment reason hidden")
	}
	for _, kind := range []string{GapScansFailed, GapLegsUnfinished, GapClaimsOpen, GapInventoryUnaccounted} {
		input.Gaps = []ReportGap{{Kind: kind, Count: 1}}
		if input.Completeness() != CompletenessIncomplete {
			t.Fatalf("assessment waived %s", kind)
		}
	}
	input.Gaps = nil
	review.Assessments[1].Disposition = reviewcoverage.EssentialOpen
	if input.Completeness() != CompletenessIncomplete || completenessReason(input, input.Completeness()) == "" || len(notCoveredItems(input)) == 0 {
		t.Fatal("essential coverage work lacks an incomplete explanation")
	}
	review.Revision = "stale"
	if input.Completeness() != CompletenessIncomplete {
		t.Fatal("stale review accepted")
	}
	input.CoverageReview = nil
	if input.Completeness() != CompletenessIncomplete {
		t.Fatal("missing review accepted")
	}
}

func TestCoverageReviewFixtureIsComplete(t *testing.T) {
	input := loadReportInputFixture(t, "security_coverage_review.json")
	if input.Completeness() != CompletenessComplete {
		t.Fatal("assessed fixture does not represent complete coverage")
	}
}

func TestOpenQuestionsUseAssessedMateriality(t *testing.T) {
	facts := reviewcoverage.Facts{
		Obligations: []reviewcoverage.Fact{{ID: "boundary"}},
		Gaps:        []reviewcoverage.Fact{{ID: "question/c6", Kind: "review_question", Subject: "c6", Obligations: []string{"boundary"}}},
	}
	facts.Seal()
	cite := []api.CitationGroundingCitedEvidence{{Handle: "read#1"}}
	for _, tc := range []struct{ question, obligation, want string }{
		{reviewcoverage.Immaterial, reviewcoverage.Satisfied, CompletenessComplete},
		{reviewcoverage.MaterialOpen, reviewcoverage.MaterialOpen, CompletenessMostly},
		{reviewcoverage.EssentialOpen, reviewcoverage.EssentialOpen, CompletenessIncomplete},
		{reviewcoverage.MaterialOpen, reviewcoverage.Satisfied, CompletenessIncomplete},
		{reviewcoverage.EssentialOpen, reviewcoverage.Satisfied, CompletenessIncomplete},
		{reviewcoverage.Covered, reviewcoverage.Satisfied, CompletenessIncomplete},
	} {
		review := api.CoverageReview{Revision: facts.Revision, Assessments: []api.CoverageAssessment{
			{ID: "boundary", Disposition: tc.obligation, Reason: "Boundary traced", CitedEvidence: cite},
			{ID: "question/c6", Disposition: tc.question, Reason: "Remaining uncertainty bounded by observed callers", Obligations: []string{"boundary"}, CitedEvidence: cite},
		}}
		input := ReportInput{CoverageFacts: &facts, CoverageReview: &review, Claims: []ReportClaim{{ID: "c6", Class: ClaimOpen}}, Gaps: []ReportGap{{Kind: GapClaimsOpen, Count: 1}}}
		if tc.want == CompletenessComplete && len(notCoveredItems(input)) != 0 {
			t.Fatal("immaterial question still presented as remaining work")
		}
		if got := input.Completeness(); got != tc.want {
			t.Fatalf("%s/%s = %s, want %s", tc.question, tc.obligation, got, tc.want)
		}
	}
}
