package presentation

import (
	workflowvalidation "github.com/lycaon/lycaon/internal/workflow/validation"
	"github.com/lycaon/lycaon/pkg/api"
)

func RunCoverageReview(verdicts []PhaseVerdict) *api.CoverageReview {
	var out *api.CoverageReview
	for _, v := range verdicts {
		if !v.Def.CarriesCoverage() {
			continue
		}
		out = nil
		out = nil
		if v.Record.GateVerdict != "approved" {
			continue
		}
		review, err := workflowvalidation.ParseVerdictCoverage(v.Def, VerdictMembers(v.Record.Artifacts))
		if err == nil {
			out = review
		}
	}
	return out
}
