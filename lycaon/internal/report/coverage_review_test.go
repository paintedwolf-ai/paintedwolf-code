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
	review := reviewcoverage.Review{Revision: facts.Revision, Assessments: []reviewcoverage.Assessment{
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
