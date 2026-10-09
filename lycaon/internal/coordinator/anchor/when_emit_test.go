package anchor_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/kick"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/testutil"
	"gopkg.in/yaml.v3"
)

func TestEmitHonorsConditionWhenFalse(t *testing.T) {
	reg := loadTestRegistry(t)
	dir := t.TempDir()
	doc := map[string]any{
		"on":       "compose.done",
		"selector": map[string]any{"surface": "coordinator"},
		"effect":   "inform",
		"render":   "coordinator-compose-done",
		"when":     "false",
		"tier":     "builtin",
	}
	raw, err := yaml.Marshal(doc)
	testutil.FailErr(t, "marshal", err)
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(dir, "compose-done.yaml"), raw, 0o644))

	loaderReg, err := anchor.LoadRegistry(extpacks.OnDisk(dir), "")
	testutil.FailErr(t, "LoadRegistry", err)
	anchor.SetDefaultRegistry(loaderReg)
	t.Cleanup(func() { anchor.SetDefaultRegistry(reg) })

	kicks := &kick.KickEngine{}
	bus := anchor.NewBus(kicks)
	bus.SetRegistry(loaderReg)
	bus.Emit(context.Background(), "sess-when", anchor.ComposeDone, anchor.Envelope{})
	if id := kicks.TakePendingKickID("sess-when"); id != "" {
		t.Fatalf("when:false must skip Emit, got pending %q", id)
	}
}

func TestEmitMatchHonorsConditionWhenPhase(t *testing.T) {
	reg := loadTestRegistry(t)
	dir := t.TempDir()
	doc := map[string]any{
		"on":       "phase.entered",
		"selector": map[string]any{"surface": "phase"},
		"effect":   "inform",
		"render":   "coordinator-phase-advanced",
		"when":     `paintedwolf.phase == "plan"`,
		"tier":     "builtin",
	}
	raw, err := yaml.Marshal(doc)
	testutil.FailErr(t, "marshal", err)
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(dir, "phase-entered.yaml"), raw, 0o644))

	loaderReg, err := anchor.LoadRegistry(extpacks.OnDisk(dir), "")
	testutil.FailErr(t, "LoadRegistry", err)
	anchor.SetDefaultRegistry(loaderReg)
	t.Cleanup(func() { anchor.SetDefaultRegistry(reg) })

	kicks := &kick.KickEngine{}
	bus := anchor.NewBus(kicks)
	bus.SetRegistry(loaderReg)

	bus.EmitMatch(context.Background(), "sess-phase", anchor.PhaseEntered, anchor.Envelope{}, anchor.MatchContext{Surface: "phase", Phase: "execute"})
	if id := kicks.TakePendingKickID("sess-phase"); id != "" {
		t.Fatalf("when phase==plan must skip execute MatchContext, got %q", id)
	}
	bus.EmitMatch(context.Background(), "sess-phase", anchor.PhaseEntered, anchor.Envelope{}, anchor.MatchContext{Surface: "phase", Phase: "plan"})
	if id := kicks.TakePendingKickID("sess-phase"); id != "coordinator-phase-advanced" {
		t.Fatalf("when phase==plan must queue, got %q", id)
	}
}

// A run context without its workflow version is a host defect: the bus
// resolves nothing rather than guessing a version, and the model is not asked
// to repair it.
func TestEmitMatchWorkflowVersionMissingFailsClosed(t *testing.T) {
	kicks := &kick.KickEngine{}
	bus := anchor.NewBus(kicks)
	bus.SetRegistry(loadTestRegistry(t))

	matchCtx := anchor.MatchContext{Surface: "phase", Workflow: "security-survey", Phase: "challenge"}
	bus.EmitMatch(context.Background(), "sess-missing-ver", anchor.PhaseEntered, anchor.Envelope{}, matchCtx)

	if kickID, ok := kicks.PeekPendingKickID("sess-missing-ver"); ok {
		t.Fatalf("missing workflow version queued %q", kickID)
	}
}
