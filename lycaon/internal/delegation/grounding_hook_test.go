package delegation

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/anchorcatalog"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/grounding"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestGroundingAfterPromptTaskResetsEscalatedState(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	sessID := "sess-coord"
	leg := api.Leg{ID: "leg-1", Title: "implement", Prompt: "do work", Status: api.LegStatusPending}
	delegation := api.Delegation{ProjectID: testdbseed.DefaultProjectID, WorkspacePath: t.TempDir(), Task: "do work", Phase: api.DelegationPhaseWorker}
	if _, err := store.Create(ctx, delegation, sessID, []api.Leg{leg}); err != nil {
		testutil.FailErr(t, "create delegation", err)
	}

	stateStore := grounding.NewStateStore()
	state := stateStore.Get(sessID)
	state.Escalated = true
	state.ConsecutiveWarnings = 2
	stateStore.Set(sessID, state)

	coord := NewGroundingCoordinator(store, nil, NewSimpleDelegationGroundingGate(DefaultGroundingConfig()), DefaultGroundingConfig(), stateStore, nil)
	err := coord.AfterPrompt(ctx, sessID, []string{"task"})
	testutil.FailErr(t, "AfterPrompt task reset", err)
	if coord.IsEscalated(sessID) {
		t.Fatal("task() on a grounded turn must reset delegation circuit breaker")
	}
}

func TestGroundingAfterPromptDelegateDispatchStillResets(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	sessID := "sess-coord"
	leg := api.Leg{ID: "leg-1", Title: "implement", Prompt: "do work", Status: api.LegStatusDispatched, WorkerID: "job-1"}
	delegation := api.Delegation{ProjectID: testdbseed.DefaultProjectID, WorkspacePath: t.TempDir(), Task: "do work", Phase: api.DelegationPhaseWorker}
	if _, err := store.Create(ctx, delegation, sessID, []api.Leg{leg}); err != nil {
		testutil.FailErr(t, "create delegation", err)
	}

	stateStore := grounding.NewStateStore()
	state := stateStore.Get(sessID)
	state.Escalated = true
	stateStore.Set(sessID, state)

	coord := NewGroundingCoordinator(store, nil, NewSimpleDelegationGroundingGate(DefaultGroundingConfig()), DefaultGroundingConfig(), stateStore, nil)
	err := coord.AfterPrompt(ctx, sessID, []string{"delegate_dispatch"})
	testutil.FailErr(t, "AfterPrompt dispatch reset", err)
	if coord.IsEscalated(sessID) {
		t.Fatal("delegate_dispatch must still reset delegation circuit breaker")
	}
}

func groundingTestPipeline(t *testing.T) *oar.GuardPipeline {
	t.Helper()
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	root := testutil.CheckoutRoot(t)
	testutil.FailErr(t, "install anchors", anchorcatalog.InstallFile(filepath.Join(root, "lycaon/config/packs/painted-wolf/platform/host/anchors/catalog.yaml")))
	loader, err := oar.NewLoader(filepath.Join(root, "schemas"))
	testutil.FailErr(t, "create loader", err)
	rules, err := loader.LoadEffectivePolicy()
	testutil.FailErr(t, "load grounding policy", err)
	pipeline := oar.NewGuardPipeline(rules, loader, oar.NewCounterStore())
	pipeline.EnableAnchor(oar.AnchorCoordinatorCloseoutCheck)
	pipeline.EnableAnchor(oar.AnchorCoordinatorPostTurn)
	return pipeline
}

func TestGroundingLifecycleRetainsCloseoutRefusalAndOnePostTurnNotice(t *testing.T) {
	store := NewMemoryStore()
	const sessionID = "grounding-lifecycle"
	delegation, err := store.Create(t.Context(), api.Delegation{ProjectID: testdbseed.DefaultProjectID, WorkspacePath: t.TempDir(), Task: "fixture", Phase: api.DelegationPhaseWorker}, sessionID, []api.Leg{{ID: "leg-1", Status: api.LegStatusComplete}})
	testutil.FailErr(t, "create delegation", err)
	cfg := DefaultGroundingConfig()
	cfg.CircuitBreaker.EscalateMode = "block"
	coord := NewGroundingCoordinator(store, nil, nil, cfg, nil, nil)
	coord.Pipeline = groundingTestPipeline(t)
	var notices []oar.Advisory
	coord.Pipeline.SetAdvisorySink(func(_ context.Context, _ string, anchor string, d *oar.Decision) error {
		if anchor != oar.AnchorCoordinatorPostTurn {
			t.Fatalf("notice delivered at %s", anchor)
		}
		notices = append(notices, d.Advisories...)
		return nil
	})
	err = coord.CheckDelegationCloseout(t.Context(), delegation.ID)
	refusal, ok := guidance.RefusalFromError(err)
	if !ok || refusal.Code() != "COORDINATOR_UNGROUNDED_CLAIM" || refusal.Copy == nil || !errors.Is(err, ErrGroundingPending) {
		t.Fatalf("closeout lost typed refusal: %v", err)
	}
	if len(notices) != 0 {
		t.Fatalf("closeout emitted post-turn notices: %v", notices)
	}
	if n := coord.Pipeline.Counters().Get(sessionID, "COORDINATOR_UNGROUNDED_CLAIM", oar.CounterBreaker); n != 1 {
		t.Fatalf("closeout count = %d", n)
	}
	testutil.FailErr(t, "ordinary post-turn check", coord.AfterPrompt(t.Context(), sessionID, nil))
	state := coord.State.Get(sessionID)
	state.Escalated = true
	coord.State.Set(sessionID, state)
	err = coord.AfterPrompt(t.Context(), sessionID, nil)
	if !errors.Is(err, session.ErrGroundingEscalated) || len(notices) != 1 || notices[0].Code != "COORDINATOR_GROUNDING_ESCALATED" || notices[0].Copy == nil {
		t.Fatalf("escalation delivery: error=%v notices=%+v", err, notices)
	}
	coord.Reset(sessionID)
	if coord.IsEscalated(sessionID) || coord.Pipeline.Counters().Get(sessionID, "COORDINATOR_UNGROUNDED_CLAIM", oar.CounterBreaker) != 0 {
		t.Fatal("reset retained grounding escalation or OAR counter")
	}
}

func TestAmbientGroundingDeliversOnceWithoutBlockingAndResetsCounters(t *testing.T) {
	cfg := DefaultGroundingConfig()
	coord := NewAmbientGroundingCoordinator(nil, nil, nil, cfg, nil, nil)
	coord.Pipeline = groundingTestPipeline(t)
	const sessionID = "ambient-lifecycle"
	var notices []oar.Advisory
	coord.Pipeline.SetAdvisorySink(func(_ context.Context, _ string, anchor string, d *oar.Decision) error {
		if anchor != oar.AnchorCoordinatorPostTurn || d.Effect != oar.EffectNudge {
			t.Fatalf("ambient decision changed boundary or effect: %s %+v", anchor, d)
		}
		notices = append(notices, d.Advisories...)
		return nil
	})
	in := AmbientGroundingInput{SessionID: sessionID, Jobs: []api.WorkerTask{{ID: "finished-job", Status: api.WorkerStatusComplete}}}
	for range 8 {
		testutil.FailErr(t, "ambient advisory", coord.applyOARAmbientGrounding(t.Context(), sessionID, ambientStateKey(sessionID), in, GroundingVerdict{Code: ambientUngroundedCompletionCode}))
	}
	if len(notices) != 8 || coord.IsEscalated(sessionID) {
		t.Fatalf("ambient delivery count=%d blocked=%v", len(notices), coord.IsEscalated(sessionID))
	}
	for _, notice := range notices {
		if notice.Code == "COORDINATOR_GROUNDING_ESCALATED" || notice.Copy == nil {
			t.Fatalf("ambient advisory lost its scope or copy: %+v", notice)
		}
	}
	in.LastTurnTools = []string{"task"}
	testutil.FailErr(t, "grounded task resets", coord.applyOARAmbientGrounding(t.Context(), sessionID, ambientStateKey(sessionID), in, GroundingVerdict{OK: true}))
	if coord.Pipeline.Counters().Get(sessionID, ambientUngroundedCompletionCode, oar.CounterBreaker) != 0 || groundingFlagged(coord.State.Get(ambientStateKey(sessionID)), cfg) {
		t.Fatal("grounded task retained advisory counters")
	}
}
