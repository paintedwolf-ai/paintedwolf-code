package wiring

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/lycaon/lycaon/internal/reviewcoverage"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func securityCoverageFixture(t *testing.T, h *Harness, ctx context.Context, runID string) string {
	t.Helper()
	run, err := h.WorkflowMgr.Get(ctx, runID)
	testutil.FailErr(t, "load coverage run", err)
	manifest, err := h.WorkflowMgr.ManifestForRunID(ctx, runID)
	testutil.FailErr(t, "load coverage manifest", err)
	facts, err := h.WorkflowMgr.CoverageFacts(ctx, run, manifest)
	testutil.FailErr(t, "load coverage facts", err)
	review := reviewcoverage.Review{Revision: facts.Revision}
	for _, rows := range [][]reviewcoverage.Fact{facts.Obligations, facts.Gaps} {
		for _, fact := range rows {
			// This wiring fixture settles scanners as failed and stubs worker execution.
			disposition := reviewcoverage.EssentialOpen
			assessment := reviewcoverage.Assessment{ID: fact.ID, Disposition: disposition,
				Reason:        "The fixture does not execute the required scans or survey workers.",
				CitedEvidence: []api.CitationGroundingCitedEvidence{{Handle: "survey#1"}}}
			for _, obligation := range facts.Obligations {
				assessment.Obligations = append(assessment.Obligations, obligation.ID)
			}
			review.Assessments = append(review.Assessments, assessment)
		}
	}
	raw, err := json.Marshal(review)
	testutil.FailErr(t, "marshal coverage review", err)
	return string(raw)
}
