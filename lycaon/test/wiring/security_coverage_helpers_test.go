package wiring

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func securityCoverageFixture(t *testing.T, h *Harness, ctx context.Context, runID string) string {
	t.Helper()
	run, err := h.Workflows.Manager.Store.Runs.Get(ctx, runID)
	testutil.FailErr(t, "load coverage run", err)
	manifest, err := h.Workflows.Manager.Resolver.ForRunID(ctx, runID)
	testutil.FailErr(t, "load coverage manifest", err)
	facts, err := h.Workflows.Manager.Coverage.CoverageFacts(ctx, run, manifest)
	testutil.FailErr(t, "load coverage facts", err)
	review := coverageReviewFixture(facts)
	raw, err := json.Marshal(review)
	testutil.FailErr(t, "marshal coverage review", err)
	return string(raw)
}
