package wiring

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// A wake the loop parks leaves the session idle at a stable epoch, with the
// parked decision in its report, which is what lets a progress wait give up;
// the same wait passes once the wake reaches a turn.
func TestSessionProgressReportsAParkedWakeAndPassesADeliveredOne(t *testing.T) {
	h := BuildForTest(t, WithRecordingLLM())
	limits := &implementLifecycleLimits{}
	h.Sessions.Manager.Limits.SetProvider(limits)
	ctx := t.Context()
	sess, err := h.CreateHarnessSession(t, wire.CreateSessionRequest{}, t.TempDir())
	testutil.FailErr(t, "create session", err)
	AttachDefaultAmbient(t, h, ctx, sess.ID)
	loop := h.Sessions.Manager.Coordinator.Runtime.CoordinatorLoop()
	h.Sessions.Manager.Coordinator.WaitForTurns(ctx)
	requests := len(h.Recording.AllRequests())

	// The coordinator loop is off, so admission denies the wake.
	loop.Nudges.Nudge(ctx, sess.ID, anchor.PhaseAdvanced, "", "", anchor.Envelope{})
	parked := sessionProgress(ctx, h.ServeApp, sess.ID)
	if !parked.Idle || !strings.Contains(parked.Report, string(anchor.PhaseAdvanced)+": denied") {
		t.Fatalf("after a denied wake: idle=%v report=\n%s\nwant idle with the denied PhaseAdvanced wake", parked.Idle, parked.Report)
	}
	if again := sessionProgress(ctx, h.ServeApp, sess.ID); !again.Idle || again.Epoch != parked.Epoch {
		t.Fatalf("parked session changed: idle=%v epoch %d → %d", again.Idle, parked.Epoch, again.Epoch)
	}
	if got := len(h.Recording.AllRequests()); got != requests {
		t.Fatalf("a denied wake made %d model requests", got-requests)
	}

	limits.enabled.Store(true)
	loop.Nudges.Nudge(ctx, sess.ID, anchor.PhaseAdvanced, "", "", anchor.Envelope{})
	WaitForSessionProgress(t, h.ServeApp, sess.ID, "the coordinator sends a request", func() bool {
		return len(h.Recording.AllRequests()) > requests
	})
	h.Sessions.Manager.Coordinator.WaitForTurns(ctx)
}
