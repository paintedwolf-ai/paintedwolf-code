package contract

import (
	"github.com/lycaon/lycaon/internal/toolcontract"

	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/pkg/api"
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
