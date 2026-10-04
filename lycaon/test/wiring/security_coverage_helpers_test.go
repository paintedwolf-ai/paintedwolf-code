package wiring

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func securityCoverageFixture(t *testing.T, h *Harness, ctx context.Context, runID string) string {
	t.Helper()
	run, err := h.WorkflowMgr.Get(ctx, runID)
	testutil.FailErr(t, "load coverage run", err)
	manifest, err := h.WorkflowMgr.ManifestForRunID(ctx, runID)
	testutil.FailErr(t, "load coverage manifest", err)
	facts, err := h.WorkflowMgr.CoverageFacts(ctx, run, manifest)
	testutil.FailErr(t, "load coverage facts", err)
	review := coverageReviewFixture(facts)
	raw, err := json.Marshal(review)
	testutil.FailErr(t, "marshal coverage review", err)
	return string(raw)
}
