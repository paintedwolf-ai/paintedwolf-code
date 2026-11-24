package turnnudges

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/oar"
	setupanchor "github.com/lycaon/lycaon/internal/testsetup/anchor"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestSpendSoftStopResolvesOARHintAndRejectsUnregisteredBareCode(t *testing.T) {
	setupanchor.Install()
	s := New(turnload.NewLedger(), nil)
	sess := &api.Session{ID: "spend-sess"}

	// Stock hint binding resolves the full OAR hint message with its host marker.
	nudge := s.SpendSoftStop(t.Context(), sess)
	if nudge.Empty() {
		t.Fatal("expected stock spend soft-stop hint nudge")
	}
	if nudge.SignalID != string(anchor.TurnSpendSoftStop) {
		t.Fatalf("signal id = %q, want %q", nudge.SignalID, anchor.TurnSpendSoftStop)
	}
	if !strings.HasPrefix(nudge.Content, "[host:coordinator-spend-soft-stop]") {
		t.Fatalf("content missing host marker: %q", nudge.Content)
	}
	if !strings.Contains(nudge.Content, "one bounded wind-down round with tools") {
		t.Fatalf("content missing wind-down instructions: %q", nudge.Content)
	}

	// When the bound registry has no registered hint for COORDINATOR_SPEND_WIND_DOWN,
	// OAR-HINT-4 produces the bare code, which SpendSoftStop must refuse so the loop
	// enters safe prose closeout instead of granting a tool round without instructions.
	emptyHintsReg := &anchor.Registry{}
	emptyHintsReg.SetRules(oar.NewRuleSet(nil))
	emptyHintsReg.IndexBinding(&anchor.Binding{
		On:       anchor.TurnSpendSoftStop,
		Selector: anchor.Selector{Surface: "coordinator"},
		Effect:   "inform",
		Render:   "coordinator-spend-soft-stop",
		HintCode: "COORDINATOR_SPEND_WIND_DOWN",
		Tier:     "builtin",
	})
	release := anchor.SetAnchorsFor(func(context.Context, string) *anchor.Registry { return emptyHintsReg })
	t.Cleanup(release)

	missing := s.SpendSoftStop(t.Context(), sess)
	if !missing.Empty() {
		t.Fatalf("SpendSoftStop with unregistered hint returned %+v, want empty nudge for safe prose closeout", missing)
	}

	// Non-required Render still follows OAR-HINT-4 bare code rendering.
	bare := s.Render(t.Context(), sess, anchor.TurnSpendSoftStop, nil)
	if bare.Content != "COORDINATOR_SPEND_WIND_DOWN" {
		t.Fatalf("Render(unregistered hint) = %q, want bare code", bare.Content)
	}
}
