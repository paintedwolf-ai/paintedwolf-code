//go:build darwin

package command_test

import (
	"context"
	"os/exec"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
)

// A listener the sandbox refuses reaches the command's observation with the
// listen recovery, beside whatever else the program probed.
func TestProgramRefusedListenerCarriesItsBind(t *testing.T) {
	if testing.Short() || !confine.Available() {
		t.Skip("darwin + seatbelt sandbox required")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is required to open a TCP listener")
	}
	confine.StartRefusalWatch(t.Context(), "")
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	for !confine.RefusalWatchLive() {
		if ctx.Err() != nil {
			t.Skip("kernel sandbox reports are not readable on this host")
		}
		time.Sleep(50 * time.Millisecond)
	}
	f := newProgramFixture(t)
	var effects tools.ToolInvocationOut
	f.Configure = func(tc *tools.ToolContext) {
		effects = tools.ToolInvocationOut{}
		tc.Effects.Out = &effects
	}
	out, err := f.call(t, t.Context(), "external -- "+python+" -m http.server 18798 --bind 0.0.0.0")
	testutil.FailErr(t, "run refused listener", err)
	if outcome := decodeOutcome(t, out); outcome.Status == 0 {
		t.Fatalf("refused listener exited 0: %+v", outcome)
	}
	observed := effects.Facts.Confine
	for _, refusal := range observed.Refusals.Refusals {
		t.Logf("refusal %s %q recovery=%s", refusal.Operation, refusal.Target, refusal.Recovery)
	}
	t.Logf("codes=%v witness=%s failed=%v", effects.Facts.Codes, observed.Refusals.Witness, observed.FailedStages)
	var bind bool
	for _, refusal := range observed.Refusals.Refusals {
		bind = bind || (refusal.Operation == "network-bind" && refusal.Recovery == confine.RecoverLocalListen && refusal.Port() == "18798")
	}
	if !bind {
		t.Fatalf("the refused bind did not reach the observation: %+v", observed.Refusals.Refusals)
	}
}
