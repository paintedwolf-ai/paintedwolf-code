//go:build darwin || linux

package command_test

import (
	"context"
	"os/exec"
	"strconv"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
)

// Invariant I10, live: a listener the sandbox withholds is reported with the
// listen recovery for its port, and nothing the program probed on the way
// routes to host execution. Where the platform's reports can be dropped, an
// unreported bind is allowed; a misrouted one never is.
func TestWithheldListenerRoutesToLocalListen(t *testing.T) {
	if testing.Short() || !confine.Available() {
		t.Skip("applied confinement required")
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
			t.Skip("sandbox refusal reports are not readable on this host")
		}
		time.Sleep(50 * time.Millisecond)
	}
	f := newProgramFixture(t)
	var effects tools.ToolInvocationOut
	f.Configure = func(tc *tools.ToolContext) {
		effects = tools.ToolInvocationOut{}
		tc.Effects.Out = &effects
	}
	port := 18700
	for _, source := range withheldListenerSources(python) {
		port++
		out, err := f.call(t, t.Context(), "external -- "+source(port))
		testutil.FailErr(t, "run withheld listener", err)
		if outcome := decodeOutcome(t, out); outcome.Status == 0 {
			t.Fatalf("%s exited 0 without its listener", source(port))
		}
		reported := false
		for _, refusal := range effects.Facts.Confine.Refusals.Refusals {
			if refusal.Recovery == confine.RecoverHostExecution {
				t.Errorf("%s: %s routes to host execution", source(port), refusal.Display())
			}
			if refusal.Operation != "network-bind" {
				continue
			}
			reported = true
			if refusal.Recovery != confine.RecoverLocalListen || refusal.Port() != strconv.Itoa(port) {
				t.Errorf("%s: listener %s routes to %s port %q, want local_listen port %d", source(port), refusal.Display(), refusal.Recovery, refusal.Port(), port)
			}
		}
		if listenerReportsAreLossless && !reported {
			t.Errorf("%s: withheld listener unreported: %+v", source(port), effects.Facts.Confine.Refusals)
		}
	}
}
