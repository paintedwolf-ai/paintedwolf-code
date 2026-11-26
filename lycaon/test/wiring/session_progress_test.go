package wiring

import (
	"fmt"
	"runtime"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// progressFailure ends the waiting goroutine on Fatalf and keeps the message.
type progressFailure struct {
	testing.TB
	message string
}

func (f *progressFailure) Helper() {}

func (f *progressFailure) Fatalf(format string, args ...any) {
	f.message = fmt.Sprintf(format, args...)
	runtime.Goexit()
}

// A host-progress wait whose wake the loop parks fails at once with the
// parked decision; the same wait passes when the wake reaches a turn.
func TestSessionProgressWaitFailsFastOnAParkedWake(t *testing.T) {
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
	requested := func() bool { return len(h.Recording.AllRequests()) > requests }

	// The coordinator loop is off, so admission denies the wake.
	loop.Nudges.Nudge(ctx, sess.ID, anchor.PhaseAdvanced, "", "", anchor.Envelope{})
	failure := &progressFailure{TB: t}
	done := make(chan struct{})
	go func() {
		defer close(done)
		WaitForSessionProgress(failure, h.ServeApp, sess.ID, "the coordinator sends a request", requested)
	}()
	testutil.AwaitClosed(t, "the parked wait ends", done)
	if !strings.HasPrefix(failure.message, "the coordinator sends a request: ") ||
		!strings.Contains(failure.message, string(anchor.PhaseAdvanced)+": denied") {
		t.Fatalf("parked wait failure = %q, want the step and the denied PhaseAdvanced wake", failure.message)
	}

	limits.enabled.Store(true)
	loop.Nudges.Nudge(ctx, sess.ID, anchor.PhaseAdvanced, "", "", anchor.Envelope{})
	WaitForSessionProgress(t, h.ServeApp, sess.ID, "the coordinator sends a request", requested)
	h.Sessions.Manager.Coordinator.WaitForTurns(ctx)
}
