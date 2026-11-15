package nfsserver_test

import (
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/layeredfs"
)

// A barrier gates its view until released, refuses a second barrier meanwhile, and leaves no gate state
// behind; a closed server reports itself gone.
func TestBarrierDrainGatesItsViewUntilReleased(t *testing.T) {
	env := newTestEnv(t, nil)
	env.createViewAndExport(t, "view-a", true)
	if got := env.Server.ResetUnstableState(); env.Server.WriteVerifier() != got {
		t.Fatal("the write verifier is not the one a reset reported")
	}

	release, err := env.Server.BeginBarrierDrain(t.Context(), "view-a")
	if err != nil {
		t.Fatalf("begin barrier: %v", err)
	}
	if gated, inFlight, _ := env.Server.GateStateForTest(); gated != 1 || inFlight != 0 {
		t.Fatalf("gate state during a barrier: gated=%d inFlight=%d", gated, inFlight)
	}
	if _, err := env.Server.BeginBarrierDrain(t.Context(), "view-a"); !errors.Is(err, layeredfs.ErrBarrierActive) {
		t.Fatalf("a second barrier on the view: %v", err)
	}
	release()
	release()
	if gated, inFlight, _ := env.Server.GateStateForTest(); gated != 0 || inFlight != 0 {
		t.Fatalf("gate state after release: gated=%d inFlight=%d", gated, inFlight)
	}

	if !env.Server.IsAlive() {
		t.Fatal("a running server reports itself gone")
	}
	if err := env.Server.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if env.Server.IsAlive() {
		t.Fatal("a closed server reports itself alive")
	}
}
