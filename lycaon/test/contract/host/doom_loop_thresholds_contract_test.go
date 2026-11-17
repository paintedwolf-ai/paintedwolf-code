package contract

import (
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/session/loopguard"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// doomLoopUnitFires evaluates a shipped loop-guard unit's condition at the
// counters set supplies.
func doomLoopUnitFires(t *testing.T, cfg *guidance.HintConfig, id string, set func(*oar.GuardContext)) bool {
	t.Helper()
	entry, ok := cfg.HintCodes[id]
	if !ok || entry.When == "" {
		t.Fatalf("shipped policy unit %s has no condition", id)
	}
	gc := oar.NewGuardContext()
	set(gc)
	fires, err := oar.EvaluateCondition(entry.When, gc)
	contractcheck.FailErr(t, "evaluate "+id, err)
	return fires
}

// The policy units fire at the counts the Go guard blocks on.
func TestDoomLoopThresholdsMatchPolicy(t *testing.T) {
	t.Parallel()
	cfg, err := guidance.LoadHintConfigStock()
	contractcheck.FailErr(t, "load policy units", err)
	repeats := func(n int) func(*oar.GuardContext) {
		return func(gc *oar.GuardContext) { gc.SetRepeatCount(int64(n)) }
	}
	codeRepeats := func(n int) func(*oar.GuardContext) {
		return func(gc *oar.GuardContext) { gc.SetCodeRejectResponses(int64(n)) }
	}
	maxAttempts, maxCodeRepeats := loopguard.DoomLoopMaxAttempts, loopguard.DoomLoopMaxCodeRepeats
	for _, tc := range []struct {
		unit  string
		set   func(*oar.GuardContext)
		fires bool
	}{
		{"DOOM_LOOP_REPEAT", repeats(maxAttempts - 1), false},
		{"DOOM_LOOP_REPEAT", repeats(maxAttempts), true},
		{"DOOM_LOOP_REPEAT_WARN", repeats(maxAttempts - 1), true},
		{"DOOM_LOOP_REPEAT_WARN", repeats(maxAttempts + 1), false},
		{"DOOM_LOOP_CODE_REPEAT", codeRepeats(maxCodeRepeats - 1), false},
		{"DOOM_LOOP_CODE_REPEAT", codeRepeats(maxCodeRepeats), true},
	} {
		if got := doomLoopUnitFires(t, cfg, tc.unit, tc.set); got != tc.fires {
			t.Errorf("%s fires = %v, want %v", tc.unit, got, tc.fires)
		}
	}
	// The per-Code escalation fires on arcs the identical-args block cannot see,
	// so it must be reachable before that block lands.
	if maxCodeRepeats >= maxAttempts {
		t.Fatalf("per-Code escalation at %d must sit below the identical-args block at %d", maxCodeRepeats, maxAttempts)
	}
}
