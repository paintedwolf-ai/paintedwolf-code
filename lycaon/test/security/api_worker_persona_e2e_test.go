package security

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/testdbseed"

	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
)

func TestDelegateImplementerPromptContainsPersona(t *testing.T) {
	rec := llm.NewRecordingClient(llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{
		Pattern: ".",
		Text:    "implementation complete",
	}}}))
	h := wiring.BuildForTest(t, wiring.WithLLMClient(rec))
	cancel := h.StartBackgroundWorkers(t, context.Background())
	t.Cleanup(cancel)
	delMgr := h.DelegationMgr
	ctx := context.Background()
	testdbseed.InsertProjectRoot(t, h.DB, testdbseed.DefaultProjectID, t.TempDir())

	r, err := delMgr.Create(ctx, api.CreateDelegationRequest{
		ProjectID: testdbseed.DefaultProjectID,
		Task:      "implement feature",
		Strategy:  api.HuntStrategyFileBased,
	})
	testutil.FailErr(t, "delMgr.Create failed", err)
	legID := r.Legs[0].ID
	if _, err := delMgr.DispatchLeg(ctx, r.ID, legID, ""); err != nil {
		testutil.FailErr(t, "delMgr.DispatchLeg failed", err)
	}

	testutil.WaitFor(t, 10*time.Second, func() bool {
		st, err := delMgr.GetStatus(ctx, r.ID)
		if err != nil {
			return false
		}
		return st.Phase == api.DelegationPhaseDone
	})

	var hasFocus, hasLeg bool
	for _, msg := range rec.LastRequest().Messages {
		if msg.Role != api.MessageRoleSystem {
			continue
		}
		if strings.Contains(msg.Content, "## Focus") {
			hasFocus = true
		}
		if strings.Contains(msg.Content, inject.WorkerLegInjectSentinel) {
			hasLeg = true
		}
	}
	if !hasFocus || !hasLeg {
		t.Fatalf("worker prompt missing persona layers: focus=%v leg=%v messages=%d", hasFocus, hasLeg, len(rec.LastRequest().Messages))
	}
}

func TestDelegatePlanReviewerDistinctFromImplementer(t *testing.T) {
	rec := llm.NewRecordingClient(llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{
		Pattern: ".",
		Text:    "implementation complete",
	}}}))
	h := wiring.BuildForTest(t, wiring.WithLLMClient(rec))
	cancel := h.StartBackgroundWorkers(t, context.Background())
	t.Cleanup(cancel)
	delMgr := h.DelegationMgr
	ctx := context.Background()
	testdbseed.InsertProjectRoot(t, h.DB, testdbseed.DefaultProjectID, t.TempDir())

	r, err := delMgr.Create(ctx, api.CreateDelegationRequest{
		ProjectID: testdbseed.DefaultProjectID,
		Task:      "review plan",
		Strategy:  api.HuntStrategyFileBased,
	})
	testutil.FailErr(t, "delMgr.Create failed", err)
	leg := r.Legs[0]
	leg.AgentType = "plan-reviewer"
	if err := delMgr.Store.UpdateLeg(ctx, leg); err != nil {
		testutil.FailErr(t, "delMgr.Store.UpdateLeg failed", err)
	}
	if _, err := delMgr.DispatchLeg(ctx, r.ID, leg.ID, ""); err != nil {
		testutil.FailErr(t, "delMgr.DispatchLeg failed", err)
	}

	testutil.WaitFor(t, 10*time.Second, func() bool {
		st, _ := delMgr.GetStatus(ctx, r.ID)
		return st.Phase == api.DelegationPhaseDone
	})

	var l1 string
	for _, msg := range rec.LastRequest().Messages {
		if msg.Role == api.MessageRoleSystem && !strings.Contains(msg.Content, inject.WorkerLegInjectSentinel) {
			l1 = msg.Content
			break
		}
	}
	if !strings.Contains(l1, "Plan Reviewer") || !strings.Contains(l1, "advisory") {
		t.Fatalf("plan-reviewer L1 = %q", l1)
	}
}
