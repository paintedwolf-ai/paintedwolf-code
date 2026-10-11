package contract

import (
	"github.com/lycaon/lycaon/internal/toolcontract"

	"context"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/kick"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	coordinatorbatch "github.com/lycaon/lycaon/test/wiring/fixtures/coordinator_batch"
)

func TestSynthesisSurfaceAfterVerifierTerminal(t *testing.T) {
	t.Parallel()
	fix := coordinatorbatch.Load(t)
	state := fix.ImplementSessionState()
	if state.BatchPhase != "synthesize" {
		t.Fatalf("fixture batch_phase = %q want synthesize", state.BatchPhase)
	}

	profile := surface.ResolveTurnProfile(
		api.CoordinatorRunContext{WorkflowID: "implement", CurrentPhase: "work"},
		&api.Session{Posture: api.SessionPostureBuild},
		fix.History,
		state,
	)
	if profile.SurfaceID == toolcontract.SurfaceImplementInvestigate {
		t.Fatal("post-verifier loop wake must not select implement_investigate")
	}
	if profile.SurfaceID != "implement_synthesis" {
		t.Fatalf("surface = %q want implement_synthesis", profile.SurfaceID)
	}
}

func TestScheduledKickDoesNotAdvanceUserIntentBoundary(t *testing.T) {
	t.Parallel()
	fix := coordinatorbatch.Load(t)

	withoutScheduled := fix.History[:len(fix.History)-2]
	withScheduled := fix.History

	before := api.UserIntentBoundary(withoutScheduled)
	after := api.UserIntentBoundary(withScheduled)
	if before != after {
		t.Fatalf("UserIntentBoundary shifted from %d to %d after internal scheduled kick", before, after)
	}
	if before != 1 {
		t.Fatalf("UserIntentBoundary = %d want 1 (anchor on visible user ask)", before)
	}
}

func TestScheduledKickOmitsStalePendingOverlays(t *testing.T) {
	fix := coordinatorbatch.Load(t)
	state := fix.ImplementSessionState()
	if len(state.PendingOverlayIDs) != 0 {
		t.Fatalf("fixture pending overlays = %v want empty ledger", state.PendingOverlayIDs)
	}

	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})

	data := map[string]any{"batch_phase": state.BatchPhase}
	contractcheck.FailErr(t, "merge coordinator kick policy vars", prompts.MergeCoordinatorKickPolicyVars(data))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := engine.RenderKick(ctx, "coordinator-scheduled", data)
	contractcheck.FailErr(t, "RenderKick coordinator-scheduled", err)
	if strings.Contains(out, "promote_overlay") || strings.Contains(out, "preview_overlay") {
		t.Fatalf("scheduled kick with empty ledger must not mention overlay tools\n--- rendered ---\n%s", out)
	}
	if strings.Contains(out, "Pending overlays") {
		t.Fatalf("scheduled kick with empty ledger must not list pending overlays\n--- rendered ---\n%s", out)
	}

	kickEngine := kick.KickEngine{}
	kickEngine.SetPromptEngine(engine)
	kickEngine.QueueDeferred("sess-batch-kick", anchor.InformRender(anchor.WaitTimerFired), kick.WithBatchSeq(state.BatchSeq))
	if id := kickEngine.TakePendingKickID("sess-batch-kick"); id != anchor.InformRender(anchor.WaitTimerFired) {
		t.Fatalf("TakePendingKickID = %q want %s", id, anchor.WaitTimerFired)
	}
	nudge, lease, ok, err := kickEngine.RenderPendingNudge(t.Context(), "sess-batch-kick", kick.CoordinatorKickRenderContext{
		BatchPhase: state.BatchPhase,
		BatchSeq:   state.BatchSeq,
	})
	contractcheck.FailErr(t, "RenderPendingNudge", err)
	if !ok || strings.TrimSpace(nudge) == "" {
		t.Fatal("deferred scheduled kick should render at take with live empty ledger")
	}
	kickEngine.AckPendingNudge("sess-batch-kick", lease)
	if strings.Contains(nudge, "promote_overlay") {
		t.Fatalf("deferred render leaked overlay promote copy: %q", nudge)
	}
}
