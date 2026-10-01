package delegation

import (
	"context"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/grounding"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestGroundingCheckTurnNeverScansProse(t *testing.T) {
	gate := NewSimpleDelegationGroundingGate(DefaultGroundingConfig())
	in := GroundingInput{
		Legs: []api.Leg{{
			ID:     "leg-1",
			Status: api.LegStatusPending,
		}},
	}
	v := gate.CheckTurn(context.Background(), in)
	if !v.OK {
		t.Fatalf("post-turn must not block a coordinator turn — grounding is enforced at closeout: %s", v.Code)
	}
}

func TestGroundingCheckTurnEscalatedBlocksOnlyInBlockMode(t *testing.T) {
	// The default escalate mode is advisory ("flag") — an escalated counter must NOT block.
	flagGate := NewSimpleDelegationGroundingGate(DefaultGroundingConfig())
	in := GroundingInput{UngroundedState: grounding.UngroundedCounter{Escalated: true}}
	if v := flagGate.CheckTurn(context.Background(), in); !v.OK {
		t.Fatalf("default (flag) mode must never block, got %+v", v)
	}
	// Hard-stop mode requires escalate_mode: block.
	cfg := DefaultGroundingConfig()
	cfg.CircuitBreaker.EscalateMode = "block"
	blockGate := NewSimpleDelegationGroundingGate(cfg)
	v := blockGate.CheckTurn(context.Background(), in)
	if v.OK || v.Code != "COORDINATOR_GROUNDING_ESCALATED" {
		t.Fatalf("escalated state must block under block mode: %+v", v)
	}
}

func TestGroundingCheckCloseoutRequiresSummaryTag(t *testing.T) {
	gate := NewSimpleDelegationGroundingGate(DefaultGroundingConfig())
	in := GroundingInput{
		DelegationID: "dep-1",
		Legs: []api.Leg{{
			ID:                 "leg-1",
			Status:             api.LegStatusComplete,
			WorkerID:           "job-1",
			CompletionCriteria: defaultCompletionCriteria,
		}},
		Jobs: []api.WorkerTask{{
			ID:     "job-1",
			LegID:  "leg-1",
			Status: api.WorkerStatusComplete,
		}},
	}
	v := gate.CheckCloseout(context.Background(), in)
	if v.OK {
		t.Fatal("expected closeout block without summary tag")
	}
}

func TestGroundingCheckCloseoutWithSummaryTag(t *testing.T) {
	gate := NewSimpleDelegationGroundingGate(DefaultGroundingConfig())
	in := GroundingInput{
		DelegationID: "dep-1",
		Legs: []api.Leg{{
			ID:                 "leg-1",
			Status:             api.LegStatusComplete,
			WorkerID:           "job-1",
			CompletionCriteria: defaultCompletionCriteria,
		}},
		Jobs: []api.WorkerTask{{
			ID:     "job-1",
			LegID:  "leg-1",
			Status: api.WorkerStatusComplete,
		}},
		SummaryTags: []WorkerSummaryTag{{
			DelegationID: "dep-1",
			LegID:        "leg-1",
			JobID:        "job-1",
			TS:           time.Now().UTC(),
		}},
	}
	v := gate.CheckCloseout(context.Background(), in)
	if !v.OK {
		t.Fatalf("expected closeout ok, got %s: %s", v.Code, v.Reason)
	}
}

func TestGroundingCheckCloseoutWaitsForOverlayLanding(t *testing.T) {
	gate := NewSimpleDelegationGroundingGate(DefaultGroundingConfig())
	in := GroundingInput{
		DelegationID: "dep-1",
		Legs: []api.Leg{{
			ID: "leg-1", Status: api.LegStatusComplete, WorkerID: "job-1",
			CompletionCriteria: defaultCompletionCriteria,
		}},
		Jobs: []api.WorkerTask{{
			ID: "job-1", LegID: "leg-1", Status: api.WorkerStatusComplete,
			MergeStatus: api.WorkerMergeStatusPending,
		}},
		SummaryTags: []WorkerSummaryTag{{
			DelegationID: "dep-1", LegID: "leg-1", JobID: "job-1", TS: time.Now().UTC(),
		}},
	}
	v := gate.CheckCloseout(t.Context(), in)
	if v.OK || v.Code != "COORDINATOR_CRITERIA_UNMET" || v.Reason != "worker overlay has not landed" {
		t.Fatalf("open overlay closeout verdict = %+v", v)
	}
}

func TestCircuitBreakerEscalates(t *testing.T) {
	cfg := DefaultGroundingConfig()
	cfg.CircuitBreaker.MaxConsecutiveUngrounded = 2
	cfg.CircuitBreaker.EscalateMode = "block" // exercise the opt-in hard-stop mechanism
	state := grounding.UngroundedCounter{}
	v := GroundingVerdict{OK: false, Code: "COORDINATOR_UNGROUNDED_CLAIM"}
	ApplyCircuitBreaker(&state, v, cfg)
	ApplyCircuitBreaker(&state, v, cfg)
	if !state.Escalated {
		t.Fatal("expected escalation after consecutive warnings")
	}
}

func TestDelegationBySessionID(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	sessID := "sess-coord"
	leg := api.Leg{ID: "leg-1", Title: "t", Prompt: "do work"}
	delegation := api.Delegation{ProjectID: testdbseed.DefaultProjectID, WorkspacePath: t.TempDir(), Task: "do work"}
	if _, err := store.Create(ctx, delegation, sessID, []api.Leg{leg}); err != nil {
		testutil.FailErr(t, "create session in store", err)
	}
	id, ok := store.DelegationBySessionID(sessID)
	if !ok || id == "" {
		t.Fatal("expected delegation by session")
	}
}

func TestDispatchLegSetsCompletionCriteria(t *testing.T) {
	mgr, store, queue, delegationMgr, reg := newDelegationTestManager(t)
	ctx := context.Background()
	dir := t.TempDir()
	projectID := seedDelegationProject(t, reg, dir)
	r, err := delegationMgr.Create(ctx, api.CreateDelegationRequest{
		ProjectID: projectID,
		Task:      "task",
		Strategy:  api.HuntStrategyFileBased,
	})
	testutil.FailErr(t, "delegationMgr.Create failed", err)
	leg, err := delegationMgr.DispatchLeg(ctx, r.ID, r.Legs[0].ID, "")
	testutil.FailErr(t, "delegationMgr.DispatchLeg failed", err)
	if len(leg.CompletionCriteria) == 0 {
		t.Fatal("expected default completion criteria")
	}
	_ = mgr
	_ = store
	_ = queue
}
