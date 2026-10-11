package session

import (
	"context"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// panicSessionWorkerAbort simulates a panic surfacing from one of the
// pluggable interfaces stopSessionTree fans into (worker abort, workflow
// stop, checkpoint cancel, runtime release, invocation-interrupt recovery).
type panicSessionWorkerAbort struct{}

func (panicSessionWorkerAbort) AbortAllWorkers(context.Context, string, string, string) error {
	panic("boom: worker abort panicked")
}

func (panicSessionWorkerAbort) AbortWorkersForRoot(context.Context, string, string, []projectroot.RootRef, string) error {
	return nil
}

// A stop panic still clears active state and releases waiters.
func TestAbortRecoversPanicAndClearsStopState(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemory()
	mgr := NewHost(st, Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, nil)
	mgr.SetSessionWorkerAbort(panicSessionWorkerAbort{})

	sess, err := st.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	testutil.FailErr(t, "mark session busy", st.SetSessionStatus(ctx, sess.ID, api.SessionStatusBusy))

	if err := mgr.Stops.Abort(ctx, sess.ID, "user stopped"); err == nil {
		t.Fatal("Abort returned no error; want the panic surfaced as an error, not swallowed or left to crash the process")
	}

	if mgr.Chats.Gate.InProgress(ctx, sess.ID) {
		t.Fatal("stop state stuck active after panic recovery; finishSessionStop did not run")
	}

	// A second Abort hangs if the first flight's active[rootID] entry or
	// flight.done close were skipped.
	done := make(chan error, 1)
	go func() { done <- mgr.Stops.Abort(ctx, sess.ID, "second stop") }()
	select {
	case err2 := <-done:
		if err2 == nil {
			t.Fatal("expected second Abort to also surface the panic")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("second Abort hung: stop state was not cleared after the first panic")
	}
}
