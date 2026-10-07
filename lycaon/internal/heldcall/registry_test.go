package heldcall

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestMain(m *testing.M) { testutil.VerifyNoLeaks(m, func() {}) }

type recorder struct {
	mu      sync.Mutex
	events  []api.BackgroundProcessEvent
	settled []string
	// settledCh, when set, receives each handle after its exit event is published.
	settledCh chan string
}

func (r *recorder) publish(_ context.Context, _, _ string, event api.BackgroundProcessEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
}

func (r *recorder) onSettle(_, handle string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.settled = append(r.settled, handle)
	if r.settledCh != nil {
		r.settledCh <- handle
	}
}

func awaitSettleCallback(t *testing.T, rec *recorder, handle string) {
	t.Helper()
	select {
	case got := <-rec.settledCh:
		if got != handle {
			t.Fatalf("settled handle = %q, want %q", got, handle)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the held call did not settle")
	}
}

func (r *recorder) snapshot() ([]api.BackgroundProcessEvent, []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]api.BackgroundProcessEvent(nil), r.events...), append([]string(nil), r.settled...)
}

func spec(tool, digest string, budget time.Duration) Spec {
	return Spec{ProjectID: "p", SessionID: "s", ToolCallID: "tc", Tool: tool, ArgsDigest: digest, Budget: budget}
}

func done(content string) Func {
	return func(context.Context) Settled {
		return Settled{Content: content, Outcome: api.ToolResultOutcomeCompleted}
	}
}

func blocked(release <-chan struct{}) Func {
	return func(ctx context.Context) Settled {
		select {
		case <-release:
			return Settled{Content: "late", Outcome: api.ToolResultOutcomeCompleted}
		case <-ctx.Done():
			return Settled{Content: "canceled", Outcome: api.ToolResultOutcomeError}
		}
	}
}

func TestCallInsideTheBudgetReturnsInlineAndLeavesNoHandle(t *testing.T) {
	rec := &recorder{}
	registry := New(rec.publish, rec.onSettle)
	outcome, err := registry.Run(t.Context(), spec("find", "a", time.Second), done("ok"))
	if err != nil {
		testutil.FailErr(t, "run", err)
	}
	if outcome.Settled == nil || outcome.Settled.Content != "ok" || outcome.Handle != "" {
		t.Fatalf("outcome = %+v, want an inline result", outcome)
	}
	if known, _ := registry.State("s", "held-1"); known {
		t.Fatal("an inline call left a handle behind")
	}
	if events, _ := rec.snapshot(); len(events) != 0 {
		t.Fatalf("inline call published %+v", events)
	}
}

func TestCallPastTheBudgetIsHeldAndSettlesOnItsOwn(t *testing.T) {
	rec := &recorder{settledCh: make(chan string, 1)}
	registry := New(rec.publish, rec.onSettle)
	release := make(chan struct{})
	request := spec("find", "a", 20*time.Millisecond)
	request.DisplayTitle = "find · **/*.go"
	outcome, err := registry.Run(t.Context(), request, blocked(release))
	if err != nil {
		testutil.FailErr(t, "run", err)
	}
	if outcome.Settled != nil || outcome.Handle != "held-1" {
		t.Fatalf("outcome = %+v, want a held handle", outcome)
	}
	if known, running := registry.State("s", outcome.Handle); !known || !running {
		t.Fatalf("state = known:%t running:%t", known, running)
	}
	if !registry.HasRunningHandles("s", []string{outcome.Handle}) || !registry.HasRunningHandles("s", nil) {
		t.Fatal("a running held call is not reported running")
	}

	status, err := registry.Status("s", outcome.Handle)
	testutil.FailErr(t, "read held target", err)
	if status.DisplayTitle != request.DisplayTitle {
		t.Fatalf("held target = %q", status.DisplayTitle)
	}

	close(release)
	awaitSettleCallback(t, rec, outcome.Handle)
	if known, running := registry.State("s", outcome.Handle); !known || running {
		t.Fatalf("settled state = known:%t running:%t", known, running)
	}
	status, err = registry.Status("s", outcome.Handle)
	if err != nil {
		testutil.FailErr(t, "status", err)
	}
	if status.Settled == nil || status.Settled.Content != "late" || status.Tool != "find" {
		t.Fatalf("status = %+v", status)
	}
	events, settled := rec.snapshot()
	if len(events) != 2 || !events[0].Running || events[1].Running || events[1].ExitCode == nil || *events[1].ExitCode != 0 {
		t.Fatalf("events = %+v, want running then exit 0", events)
	}
	if len(settled) != 1 || settled[0] != outcome.Handle {
		t.Fatalf("settled callbacks = %v", settled)
	}
	list := registry.List("s")
	if len(list) != 1 || list[0].Running || list[0].ExitCode == nil {
		t.Fatalf("list = %+v", list)
	}
}

func TestCallSettlingDuringPromotionPublishesRunningBeforeExit(t *testing.T) {
	rec := &recorder{settledCh: make(chan string, 1)}
	release := make(chan struct{})
	var registry *Registry
	registry = New(func(ctx context.Context, projectID, sessionID string, event api.BackgroundProcessEvent) {
		if event.Running {
			close(release)
			if status, err := registry.Await(ctx, sessionID, event.ProcessID, 5*time.Second); err != nil || status.Running {
				t.Errorf("await during promotion = %+v, %v", status, err)
			}
		}
		rec.publish(ctx, projectID, sessionID, event)
	}, rec.onSettle)
	outcome, err := registry.Run(t.Context(), spec("find", "a", 5*time.Millisecond), blocked(release))
	if err != nil {
		testutil.FailErr(t, "run", err)
	}
	awaitSettleCallback(t, rec, outcome.Handle)
	events, _ := rec.snapshot()
	if len(events) != 2 || !events[0].Running || events[1].Running {
		t.Fatalf("events = %+v, want running then exit", events)
	}
}

func TestIdenticalRunningCallReturnsItsHandle(t *testing.T) {
	registry := New(nil, nil)
	release := make(chan struct{})
	defer close(release)
	first, err := registry.Run(t.Context(), spec("grep", "same", 10*time.Millisecond), blocked(release))
	if err != nil {
		testutil.FailErr(t, "first run", err)
	}
	_, err = registry.Run(t.Context(), spec("grep", "same", 10*time.Millisecond), blocked(release))
	var duplicate *DuplicateError
	if !errors.As(err, &duplicate) || duplicate.Handle != first.Handle {
		t.Fatalf("second run error = %v, want a duplicate of %s", err, first.Handle)
	}
	other, err := registry.Run(t.Context(), spec("grep", "different", 10*time.Millisecond), blocked(release))
	if err != nil || other.Handle == first.Handle {
		t.Fatalf("different arguments = %+v, %v", other, err)
	}
}

func TestSessionCapacityBoundsHeldCalls(t *testing.T) {
	registry := New(nil, nil)
	release := make(chan struct{})
	defer close(release)
	for i := range MaxRunningPerSession {
		digest := string(rune('a' + i))
		if _, err := registry.Run(t.Context(), spec("find", digest, 5*time.Millisecond), blocked(release)); err != nil {
			testutil.FailErr(t, "fill capacity", err)
		}
	}
	_, err := registry.Run(t.Context(), spec("find", "overflow", 5*time.Millisecond), blocked(release))
	var capacity *CapacityError
	if !errors.As(err, &capacity) || len(capacity.Handles) != MaxRunningPerSession {
		t.Fatalf("overflow error = %v", err)
	}
}

func TestStopCancelsTheHeldCall(t *testing.T) {
	registry := New(nil, nil)
	release := make(chan struct{})
	defer close(release)
	outcome, err := registry.Run(t.Context(), spec("find", "a", 5*time.Millisecond), blocked(release))
	if err != nil {
		testutil.FailErr(t, "run", err)
	}
	stop, err := registry.Stop("s", outcome.Handle)
	if err != nil || !stop.Running {
		t.Fatalf("stop = %+v, %v", stop, err)
	}
	deadline := time.After(2 * time.Second)
	for {
		status, statusErr := registry.Status("s", outcome.Handle)
		if statusErr != nil {
			testutil.FailErr(t, "status", statusErr)
		}
		if status.Settled != nil {
			if status.Settled.Content != "canceled" || !status.Stopped {
				t.Fatalf("status = %+v", status)
			}
			return
		}
		select {
		case <-deadline:
			t.Fatal("stop did not cancel the call")
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func TestTurnCancellationCancelsAnUnheldCall(t *testing.T) {
	registry := New(nil, nil)
	ctx, cancel := context.WithCancel(t.Context())
	release := make(chan struct{})
	defer close(release)
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	outcome, err := registry.Run(ctx, spec("find", "a", time.Minute), blocked(release))
	if err != nil {
		testutil.FailErr(t, "run", err)
	}
	if outcome.Settled == nil || outcome.Settled.Content != "canceled" {
		t.Fatalf("outcome = %+v, want the canceled result", outcome)
	}
}

func TestPanickingCallSettlesAsAnError(t *testing.T) {
	registry := New(nil, nil)
	outcome, err := registry.Run(t.Context(), spec("find", "a", time.Second), func(context.Context) Settled { panic("boom") })
	if err != nil {
		testutil.FailErr(t, "run", err)
	}
	if outcome.Settled == nil || outcome.Settled.Outcome != api.ToolResultOutcomeError {
		t.Fatalf("outcome = %+v", outcome)
	}
}

func TestSettledResultsAreRetainedUpToTheBound(t *testing.T) {
	registry := New(nil, nil)
	var handles []string
	for i := range MaxSettledPerSession + 3 {
		digest := string(rune('a' + i))
		release := make(chan struct{})
		outcome, err := registry.Run(t.Context(), spec("find", digest, 5*time.Millisecond), func(ctx context.Context) Settled {
			<-release
			return Settled{Content: digest, Outcome: api.ToolResultOutcomeCompleted}
		})
		close(release)
		if err != nil {
			testutil.FailErr(t, "run", err)
		}
		if outcome.Handle == "" {
			t.Fatal("blocked call returned without a handle")
		}
		handles = append(handles, outcome.Handle)
		waitSettled(t, registry, outcome.Handle)
	}
	if _, err := registry.Status("s", handles[0]); !errors.Is(err, ErrNotFound) {
		t.Fatalf("oldest result error = %v, want it evicted", err)
	}
	if _, err := registry.Status("s", handles[len(handles)-1]); err != nil {
		testutil.FailErr(t, "newest result", err)
	}
}

func waitSettled(t *testing.T, registry *Registry, handle string) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		if known, running := registry.State("s", handle); !known || !running {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("%s did not settle", handle)
		case <-time.After(2 * time.Millisecond):
		}
	}
}

func TestDisposeSessionCancelsAndForgetsHeldCalls(t *testing.T) {
	registry := New(nil, nil)
	release := make(chan struct{})
	defer close(release)
	outcome, err := registry.Run(t.Context(), spec("find", "a", 5*time.Millisecond), blocked(release))
	if err != nil {
		testutil.FailErr(t, "run", err)
	}
	registry.DisposeSession("s")
	if known, _ := registry.State("s", outcome.Handle); known {
		t.Fatal("a disposed session still holds the call")
	}
	if err := registry.Close(t.Context()); err != nil {
		testutil.FailErr(t, "close", err)
	}
}
