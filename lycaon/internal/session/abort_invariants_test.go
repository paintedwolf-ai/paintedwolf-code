package session

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/session/lifecycle"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type capturingWorkerAbort struct {
	called    atomic.Bool
	calledCh  chan struct{}
	calledOne sync.Once
	sessionID string
	projectID string
	reason    string
	err       error
}

func (c *capturingWorkerAbort) AbortAllWorkers(_ context.Context, sessionID, projectDir, reason string) error {
	c.called.Store(true)
	if c.calledCh != nil {
		c.calledOne.Do(func() { close(c.calledCh) })
	}
	c.sessionID = sessionID
	c.projectID = projectDir
	c.reason = reason
	return c.err
}

func (c *capturingWorkerAbort) AbortWorkersForRoot(_ context.Context, _, _ string, _ []projectroot.RootRef, reason string) error {
	c.called.Store(true)
	if c.calledCh != nil {
		c.calledOne.Do(func() { close(c.calledCh) })
	}
	c.reason = reason
	return c.err
}

func TestAbortRequiresSessionID(t *testing.T) {
	mgr := NewHost(store.NewMemory(), Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, nil)
	err := mgr.Stops.Abort(context.Background(), "", "")
	if err == nil {
		t.Fatal("expected error for empty session id")
	}
	err = mgr.Stops.Abort(context.Background(), "   ", "")
	if err == nil {
		t.Fatal("expected error for whitespace-only session id")
	}
}

func TestAbortSignalsAndStopsWorkersBeforeSessionRuntimeGate(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemory()
	mgr := NewHost(st, Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, nil)
	sess, err := st.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	testutil.FailErr(t, "mark busy", st.SetSessionStatus(ctx, sess.ID, api.SessionStatusBusy))

	canceled := make(chan struct{})
	var cancelOnce sync.Once
	mgr.Runner.Execution.RegisterCancel(sess.ID, func() { cancelOnce.Do(func() { close(canceled) }) })
	abort := &capturingWorkerAbort{calledCh: make(chan struct{})}
	mgr.SetSessionWorkerAbort(abort)
	gate := mgr.Runner.Execution.Prompt.Acquire(sess.ID)
	gate.Lock()
	done := make(chan error, 1)
	go func() { done <- mgr.Stops.Abort(ctx, sess.ID, "stop") }()
	select {
	case <-canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("abort did not signal prompt cancellation before waiting for the session gate")
	}
	select {
	case <-abort.calledCh:
	case <-time.After(5 * time.Second):
		t.Fatal("worker teardown did not begin while the turn was releasing the session gate")
	}
	got, err := st.Get(ctx, sess.ID)
	testutil.FailErr(t, "get gated session", err)
	if got.Status != api.SessionStatusBusy {
		t.Fatalf("status = %s before gate release, want busy", got.Status)
	}
	gate.Unlock()
	testutil.FailErr(t, "abort", <-done)
	if !abort.called.Load() {
		t.Fatal("worker teardown did not run after the session gate opened")
	}
}

func TestAbortPropagatesUserReason(t *testing.T) {
	ctx := context.Background()
	store := store.NewMemory()
	mgr := NewHost(store, Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, nil)
	abort := &capturingWorkerAbort{}
	mgr.SetSessionWorkerAbort(abort)

	sess, err := store.Create(ctx, api.CreateSessionRequest{
		Posture: api.SessionPostureBuild,
	}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "Create session", err)
	testutil.FailErr(t, "SetSessionStatus busy", store.SetSessionStatus(ctx, sess.ID, api.SessionStatusBusy))

	testutil.FailErr(t, "Abort", mgr.Stops.Abort(ctx, sess.ID, "explicit-stop"))
	if abort.reason != "explicit-stop" {
		t.Fatalf("expected reason propagated to worker abort, got %q", abort.reason)
	}
}

func TestAbortDefaultsReasonWhenBlank(t *testing.T) {
	ctx := context.Background()
	store := store.NewMemory()
	mgr := NewHost(store, Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, nil)
	abort := &capturingWorkerAbort{}
	mgr.SetSessionWorkerAbort(abort)

	sess, err := store.Create(ctx, api.CreateSessionRequest{
		Posture: api.SessionPostureBuild,
	}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "Create session", err)

	testutil.FailErr(t, "Abort", mgr.Stops.Abort(ctx, sess.ID, "   "))
	if strings.TrimSpace(abort.reason) == "" {
		t.Fatalf("expected reason filled with default, got %q", abort.reason)
	}
}

func TestAbortWithoutWorkerAbortHookStillMarksIdle(t *testing.T) {
	ctx := context.Background()
	store := store.NewMemory()
	mgr := NewHost(store, Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, nil)
	// No SetSessionWorkerAbort: abort still marks the session idle.

	sess, err := store.Create(ctx, api.CreateSessionRequest{
		Posture: api.SessionPostureBuild,
	}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "Create session", err)
	testutil.FailErr(t, "SetSessionStatus busy", store.SetSessionStatus(ctx, sess.ID, api.SessionStatusBusy))

	testutil.FailErr(t, "Abort", mgr.Stops.Abort(ctx, sess.ID, ""))
	got, err := store.Get(ctx, sess.ID)
	testutil.FailErr(t, "Get session", err)
	if got.Status != api.SessionStatusIdle {
		t.Fatalf("status = %q, want idle even with no worker abort hook", got.Status)
	}
}

func TestAbortReportsWorkerAbortErrorAndDoesNotPublishFalseIdle(t *testing.T) {
	ctx := context.Background()
	store := store.NewMemory()
	mgr := NewHost(store, Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, nil)
	abort := &capturingWorkerAbort{err: context.DeadlineExceeded}
	mgr.SetSessionWorkerAbort(abort)

	sess, err := store.Create(ctx, api.CreateSessionRequest{
		Posture: api.SessionPostureBuild,
	}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "Create session", err)
	testutil.FailErr(t, "SetSessionStatus busy", store.SetSessionStatus(ctx, sess.ID, api.SessionStatusBusy))

	err = mgr.Stops.Abort(ctx, sess.ID, "")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Abort error = %v, want deadline exceeded", err)
	}
	got, err := store.Get(ctx, sess.ID)
	testutil.FailErr(t, "Get session", err)
	if got.Status == api.SessionStatusIdle {
		t.Fatalf("status = %q, must not claim idle after incomplete teardown", got.Status)
	}
}

type blockingWorkflowStop struct {
	calls   atomic.Int32
	started chan struct{}
	release chan struct{}
}

func (s *blockingWorkflowStop) StopSession(context.Context, string, string) error {
	if s.calls.Add(1) == 1 {
		close(s.started)
	}
	<-s.release
	return nil
}

func TestConcurrentAbortCallsShareOneStopFlight(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemory()
	mgr := NewHost(st, Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, nil)
	sess, err := st.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	stop := &blockingWorkflowStop{started: make(chan struct{}), release: make(chan struct{})}
	mgr.Stops.SetWorkflowStop(stop)

	first := make(chan error, 1)
	go func() { first <- mgr.Stops.Abort(ctx, sess.ID, "stop") }()
	<-stop.started
	admitted := false
	if err := mgr.Chats.Gate.WithSessionTreeAdmission(ctx, sess.ID, func() error {
		admitted = true
		return nil
	}); !errors.Is(err, lifecycle.ErrStopping) {
		t.Fatalf("admission error = %v, want session stopping", err)
	}
	if admitted {
		t.Fatal("new session-tree work was admitted during stop")
	}
	followerCtx, cancelFollower := context.WithCancel(ctx)
	cancelFollower()
	if err := mgr.Stops.Abort(followerCtx, sess.ID, "stop"); !errors.Is(err, context.Canceled) {
		t.Fatalf("follower abort error = %v, want context canceled", err)
	}
	close(stop.release)
	testutil.FailErr(t, "first abort", <-first)
	if got := stop.calls.Load(); got != 1 {
		t.Fatalf("workflow stop calls = %d, want one", got)
	}
}

func TestPreStopTurnCannotDrainAfterStopCompletes(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemory()
	mgr := NewHost(st, Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, nil)
	sess, err := st.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	turn, err := mgr.Chats.Gate.Capture(ctx, sess.ID)
	testutil.FailErr(t, "capture turn", err)
	flight, leader := mgr.Chats.Gate.Begin(sess.ID)
	if !leader {
		t.Fatal("first stop was not leader")
	}
	mgr.Chats.Gate.Finish(sess.ID, flight, nil)
	if mgr.Chats.Gate.MayDrain(turn) {
		t.Fatal("pre-stop turn was allowed to drain queued or loop-wake work")
	}
}

func TestAbortUnknownSessionReturnsError(t *testing.T) {
	mgr := NewHost(store.NewMemory(), Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, nil)
	err := mgr.Stops.Abort(context.Background(), "no-such-session", "")
	if err == nil {
		t.Fatal("expected error for unknown session id")
	}
}

func TestAbortLeaderOutlivesRequestCancellation(t *testing.T) {
	st := store.NewMemory()
	mgr := NewHost(st, Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, nil)
	sess, err := st.Create(t.Context(), api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	testutil.FailErr(t, "mark busy", st.SetSessionStatus(t.Context(), sess.ID, api.SessionStatusBusy))
	requestCtx, cancel := context.WithCancel(t.Context())
	cancel()
	testutil.FailErr(t, "abort after request cancellation", mgr.Stops.Abort(requestCtx, sess.ID, "stop"))
	stopped, err := st.Get(t.Context(), sess.ID)
	testutil.FailErr(t, "get stopped session", err)
	if stopped.Status != api.SessionStatusIdle {
		t.Fatalf("status = %q, want idle", stopped.Status)
	}
}

func TestAbortPropagatesProjectDirToWorkerAbort(t *testing.T) {
	ctx := context.Background()
	store := store.NewMemory()
	mgr := NewHost(store, Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, nil)
	abort := &capturingWorkerAbort{}
	mgr.SetSessionWorkerAbort(abort)

	// Session.ProjectDir is the 4th positional arg to Create, not req.ProjectDir —
	// that's what Abort reads back to feed the worker abort hook.
	projectDir := t.TempDir()
	sess, err := store.Create(ctx, api.CreateSessionRequest{
		Posture: api.SessionPostureBuild,
	}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "Create session", err)
	testdbseed.BindSessionWorkspace(t, store, sess.ID, projectDir)
	sess, err = store.Get(ctx, sess.ID)
	testutil.FailErr(t, "Get session", err)

	testutil.FailErr(t, "Abort", mgr.Stops.Abort(ctx, sess.ID, ""))
	if abort.projectID != testdbseed.DefaultProjectID {
		t.Fatalf("worker abort projectID=%q want %q", abort.projectID, testdbseed.DefaultProjectID)
	}
}

type treeOnlyStopStore struct{ *store.Memory }

func (s treeOnlyStopStore) List(context.Context) ([]*api.Session, error) {
	return nil, errors.New("stop must not list unrelated sessions")
}

func TestAbortDoesNotListUnrelatedSessions(t *testing.T) {
	st := treeOnlyStopStore{Memory: store.NewMemory()}
	root, err := st.Create(t.Context(), api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create stop root", err)
	_, err = st.CreateChild(t.Context(), root, api.SpawnChildRequest{AgentType: "implementer"})
	testutil.FailErr(t, "create stop child", err)
	mgr := NewHost(st, Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, nil)
	testutil.FailErr(t, "stop tree without global session list", mgr.Stops.Abort(t.Context(), root.ID, "user stopped"))
}
