package events

import (
	"encoding/json"
	"testing"
	"testing/synctest"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestSessionHostErrorsSurviveLifecycleDebounce(t *testing.T) {
	for _, raw := range []bool{false, true} {
		name := "typed"
		if raw {
			name = "outbox JSON"
		}
		t.Run(name, func(t *testing.T) {
			hub := NewMemoryHub()
			boundary := hub.CurrentCursor()
			ch, unsub, err := hub.Subscribe(t.Context(), Subscription{Project: "84a676a3-ccd2-5967-97ed-fd384c0b9003", Viewer: testutil.HostOwner()})
			testutil.FailErr(t, "subscribe", err)
			defer unsub()
			publishSessionTestEvent(t, hub, raw, 1, api.SessionStatusBusy, "")
			publishSessionTestEvent(t, hub, raw, 2, api.SessionStatusIdle, "provider_not_configured")
			publishSessionTestEvent(t, hub, raw, 3, api.SessionStatusIdle, "prompt_failed")
			publishSessionTestEvent(t, hub, raw, 4, api.SessionStatusIdle, "")
			hub.FlushDebounced()

			if env := testutil.Receive(t, "busy edge", ch); env.EntityRevision != 1 {
				t.Fatalf("first revision = %d, want the busy edge", env.EntityRevision)
			}
			for _, want := range []string{"provider_not_configured", "prompt_failed", ""} {
				env := testutil.Receive(t, "session event", ch)
				var event api.SessionEvent
				testutil.FailErr(t, "decode session event", json.Unmarshal(env.Data, &event))
				if event.Status != api.SessionStatusIdle {
					t.Fatalf("status = %q, want idle", event.Status)
				}
				got := ""
				if event.HostError != nil {
					got = string(event.HostError.Code)
				}
				if got != want {
					t.Fatalf("host error = %q, want %q", got, want)
				}
			}
			replay, cancel, err := hub.Subscribe(t.Context(), Subscription{Project: "84a676a3-ccd2-5967-97ed-fd384c0b9003", Viewer: testutil.HostOwner(), After: boundary})
			testutil.FailErr(t, "replay host errors", err)
			defer cancel()
			for _, want := range []uint64{1, 2, 3, 4} {
				if env := testutil.Receive(t, "replayed session event", replay); env.EntityRevision != want {
					t.Fatalf("replayed revision = %d, want %d", env.EntityRevision, want)
				}
			}
		})
	}
}

func TestSessionHostErrorPreservesNewerPendingLifecycle(t *testing.T) {
	hub := NewMemoryHub()
	ch, unsub, err := hub.Subscribe(t.Context(), Subscription{Project: "84a676a3-ccd2-5967-97ed-fd384c0b9003", Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "subscribe", err)
	defer unsub()
	publishSessionTestEvent(t, hub, false, 3, api.SessionStatusIdle, "")
	// An update within the idle status coalesces behind the host error.
	publishSessionTestEvent(t, hub, false, 5, api.SessionStatusIdle, "")
	publishSessionTestEvent(t, hub, false, 4, api.SessionStatusIdle, "prompt_failed")
	hub.FlushDebounced()
	for _, want := range []uint64{3, 4, 5} {
		if env := testutil.Receive(t, "session event", ch); env.EntityRevision != want {
			t.Fatalf("revision = %d, want %d", env.EntityRevision, want)
		}
	}
}

func TestSessionHostErrorCancelsPendingLifecycleTimer(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		hub := NewMemoryHub()
		ch, unsub, err := hub.Subscribe(t.Context(), Subscription{Project: "84a676a3-ccd2-5967-97ed-fd384c0b9003", Viewer: testutil.HostOwner()})
		testutil.FailErr(t, "subscribe", err)
		defer unsub()
		publishSessionTestEvent(t, hub, false, 1, api.SessionStatusIdle, "")
		testutil.Receive(t, "initial status", ch)
		publishSessionTestEvent(t, hub, false, 2, api.SessionStatusIdle, "")
		publishSessionTestEvent(t, hub, false, 3, api.SessionStatusIdle, "prompt_failed")
		if env := testutil.Receive(t, "host error", ch); env.EntityRevision != 3 {
			t.Fatalf("host error revision = %d, want 3", env.EntityRevision)
		}
		time.Sleep(DebounceSession)
		synctest.Wait()
		select {
		case env := <-ch:
			t.Fatalf("superseded lifecycle event delivered: %+v", env)
		default:
		}
		publishSessionTestEvent(t, hub, false, 4, api.SessionStatusBusy, "")
		synctest.Wait()
		if env := receiveNow(t, ch); env.EntityRevision != 4 {
			t.Fatalf("lifecycle revision = %d, want 4", env.EntityRevision)
		}
	})
}

func TestSessionStatusEdgesDeliverBeforeTheirDebounce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		hub := NewMemoryHub()
		ch, unsub, err := hub.Subscribe(t.Context(), Subscription{Project: "84a676a3-ccd2-5967-97ed-fd384c0b9003", Viewer: testutil.HostOwner()})
		testutil.FailErr(t, "subscribe", err)
		defer unsub()
		publishSessionLifecycle(t, hub, 1, api.SessionStatusIdle, "")
		testutil.Receive(t, "first status", ch)

		// Busy commits before the turn's transcript rows, so it cannot wait for a quiet window.
		publishSessionLifecycle(t, hub, 2, api.SessionStatusBusy, "")
		synctest.Wait()
		if env := receiveNow(t, ch); env.EntityRevision != 2 {
			t.Fatalf("busy revision = %d, want 2 without waiting", env.EntityRevision)
		}

		// Updates within busy still coalesce.
		publishSessionLifecycle(t, hub, 3, api.SessionStatusBusy, "")
		publishSessionLifecycle(t, hub, 4, api.SessionStatusBusy, "")
		synctest.Wait()
		select {
		case env := <-ch:
			t.Fatalf("same-status update delivered before its window: revision %d", env.EntityRevision)
		default:
		}
		time.Sleep(DebounceSession)
		synctest.Wait()
		if env := receiveNow(t, ch); env.EntityRevision != 4 {
			t.Fatalf("coalesced revision = %d, want 4", env.EntityRevision)
		}
	})
}

func TestSessionIdleDispositionSurvivesTheNextTurn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		hub := NewMemoryHub()
		ch, unsub, err := hub.Subscribe(t.Context(), Subscription{Project: "84a676a3-ccd2-5967-97ed-fd384c0b9003", Viewer: testutil.HostOwner()})
		testutil.FailErr(t, "subscribe", err)
		defer unsub()
		publishSessionLifecycle(t, hub, 1, api.SessionStatusBusy, "")
		publishSessionLifecycle(t, hub, 2, api.SessionStatusIdle, api.SessionIdleDispositionCompleted)
		publishSessionLifecycle(t, hub, 3, api.SessionStatusBusy, "")
		synctest.Wait()

		want := []struct {
			status      api.SessionStatus
			disposition api.SessionIdleDisposition
		}{
			{api.SessionStatusBusy, ""},
			{api.SessionStatusIdle, api.SessionIdleDispositionCompleted},
			{api.SessionStatusBusy, ""},
		}
		for i, edge := range want {
			env := receiveNow(t, ch)
			var event api.SessionEvent
			testutil.FailErr(t, "decode session edge", json.Unmarshal(env.Data, &event))
			if event.Status != edge.status || event.IdleDisposition != edge.disposition {
				t.Fatalf("edge %d = %s/%s, want %s/%s", i, event.Status, event.IdleDisposition, edge.status, edge.disposition)
			}
		}
	})
}

func TestPromptPendingChangesDeliverBeforeTheirDebounce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		hub := NewMemoryHub()
		ch, unsub, err := hub.Subscribe(t.Context(), Subscription{Project: "84a676a3-ccd2-5967-97ed-fd384c0b9003", Viewer: testutil.HostOwner()})
		testutil.FailErr(t, "subscribe", err)
		defer unsub()
		publishSessionLifecycle(t, hub, 1, api.SessionStatusIdle, "")
		testutil.Receive(t, "first status", ch)

		publishPendingSession := func(revision uint64, pending bool, title string) {
			t.Helper()
			testutil.FailErr(t, "publish session update", hub.Publish(t.Context(), api.EventTopicSession,
				PublishKey{Project: "84a676a3-ccd2-5967-97ed-fd384c0b9003", Session: "session-a", EntityRevision: revision},
				api.SessionEvent{
					ID: "session-a", ProjectID: "84a676a3-ccd2-5967-97ed-fd384c0b9003", Action: api.SessionEventActionUpdated,
					Status: api.SessionStatusIdle, PromptPending: pending, Title: title,
				}))
		}
		// An admitted prompt waits, then closes without a turn, then the title lands.
		publishPendingSession(2, true, "")
		publishPendingSession(3, false, "")
		publishPendingSession(4, false, "Renamed")
		synctest.Wait()

		for _, want := range []bool{true, false} {
			env := receiveNow(t, ch)
			var event api.SessionEvent
			testutil.FailErr(t, "decode pending edge", json.Unmarshal(env.Data, &event))
			if event.PromptPending != want {
				t.Fatalf("revision %d prompt_pending = %v, want %v before any coalescing", env.EntityRevision, event.PromptPending, want)
			}
		}
		select {
		case env := <-ch:
			t.Fatalf("revision %d delivered inside its window; same-state updates coalesce", env.EntityRevision)
		default:
		}
		time.Sleep(DebounceSession)
		synctest.Wait()
		if env := receiveNow(t, ch); env.EntityRevision != 4 {
			t.Fatalf("revision = %d, want the same-state update after its window", env.EntityRevision)
		}
	})
}

func receiveNow(t *testing.T, ch <-chan api.EventEnvelope) api.EventEnvelope {
	t.Helper()
	select {
	case env, ok := <-ch:
		if !ok {
			t.Fatal("subscription closed")
		}
		return env
	default:
		t.Fatal("no session event was delivered")
		return api.EventEnvelope{}
	}
}

func publishSessionLifecycle(t *testing.T, hub *MemoryHub, revision uint64, status api.SessionStatus, disposition api.SessionIdleDisposition) {
	t.Helper()
	testutil.FailErr(t, "publish session lifecycle", hub.Publish(t.Context(), api.EventTopicSession,
		PublishKey{Project: "84a676a3-ccd2-5967-97ed-fd384c0b9003", Session: "session-a", EntityRevision: revision},
		api.SessionEvent{
			ID: "session-a", ProjectID: "84a676a3-ccd2-5967-97ed-fd384c0b9003", Action: api.SessionEventActionUpdated,
			Status: status, IdleDisposition: disposition,
		}))
}

func publishSessionTestEvent(t *testing.T, hub *MemoryHub, raw bool, revision uint64, status api.SessionStatus, code api.NoticeCode) {
	t.Helper()
	event := api.SessionEvent{
		ID: "session-a", ProjectID: "84a676a3-ccd2-5967-97ed-fd384c0b9003", Action: api.SessionEventActionUpdated, Status: status,
	}
	if code != "" {
		event.HostError = &api.SessionHostError{Code: code}
	}
	var payload any = event
	if raw {
		encoded, err := json.Marshal(event)
		testutil.FailErr(t, "encode outbox session event", err)
		payload = json.RawMessage(encoded)
	}
	testutil.FailErr(t, "publish session event", hub.Publish(t.Context(), api.EventTopicSession,
		PublishKey{Project: "84a676a3-ccd2-5967-97ed-fd384c0b9003", Session: "session-a", EntityRevision: revision}, payload))
}
