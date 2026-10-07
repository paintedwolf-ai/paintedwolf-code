package contract

import (
	"fmt"
	"github.com/lycaon/lycaon/internal/reviewcoverage"
	scancoverage "github.com/lycaon/lycaon/internal/scan/coverage"
	"github.com/lycaon/lycaon/pkg/api"
)

func promptBudgetCoverageAssignment() *reviewcoverage.Assignment {
	var warnings []api.ScanWarning
	for i := range 36 {
		warnings = append(warnings, api.ScanWarning{File: fmt.Sprintf("component-%02d/src/entry.go", i), Construct: "indirect_call"})
	}
	scope := scancoverage.Summarize(warnings)
	facts := reviewcoverage.Facts{Obligations: []reviewcoverage.Fact{{ID: "survey/entry", Kind: "planned_area", Subject: "External entry points"}}, Gaps: []reviewcoverage.Fact{{ID: "gap/partial", Kind: "file_partial_semantics", FileCount: scope.Files, Count: scope.Warnings, Paths: scope.PathsSample, Distribution: &scope.Profile, Scans: []string{"scan-fixture"}}}}
	candidate := api.CoverageReview{Revision: "candidate", Assessments: []api.CoverageAssessment{{ID: "gap/partial", Disposition: "immaterial", Reason: "Candidate excludes the affected scope", Obligations: []string{"survey/entry"}, CitedEvidence: []api.CitationGroundingCitedEvidence{{Handle: "scan#1"}}}}}
	assignment := reviewcoverage.Assign(facts, candidate, "challenge")
	return &assignment
}
