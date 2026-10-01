package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/people"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestHubPublishSubscribe(t *testing.T) {
	hub := NewMemoryHub()
	ctx := context.Background()

	ch, unsubscribe, err := hub.Subscribe(ctx, Subscription{Project: "54f6d2ef-89c3-56e9-9a76-f6b1a2a54476", Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "hub.Subscribe failed", err)
	defer unsubscribe()

	hub.Publish(ctx, api.EventTopicSession, PublishKey{Project: "54f6d2ef-89c3-56e9-9a76-f6b1a2a54476"}, api.SessionEvent{
		ID: "session-1", ProjectID: "54f6d2ef-89c3-56e9-9a76-f6b1a2a54476", Action: api.SessionEventActionUpdated, Status: api.SessionStatusIdle,
	})
	hub.FlushDebounced()

	select {
	case envelope := <-ch:
		if envelope.Topic != api.EventTopicSession {
			t.Fatalf("topic=%q want session", envelope.Topic)
		}
		var event api.SessionEvent
		if err := json.Unmarshal(envelope.Data, &event); err != nil {
			testutil.FailErr(t, "unmarshal JSON document", err)
		}
		if event.ID != "session-1" {
			t.Fatalf("id=%q want session-1", event.ID)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for event")
	}
}

func TestHubRejectsUnscopedProjectTopic(t *testing.T) {
	hub := NewMemoryHub()
	err := hub.Publish(t.Context(), api.EventTopicProgress, PublishKey{Session: "session-1"}, api.ProgressEvent{Revision: 1})
	if err == nil {
		t.Fatal("project topic accepted without project scope")
	}
}

func TestSubscribeReplaysProjectEventsThenContinuesLive(t *testing.T) {
	hub := NewMemoryHub()
	boundary := hub.CurrentCursor()
	for _, event := range []struct {
		project  string
		revision uint64
	}{{"54f6d2ef-89c3-56e9-9a76-f6b1a2a54476", 1}, {"4e462c43-fa37-57eb-a684-39941434b792", 2}, {"54f6d2ef-89c3-56e9-9a76-f6b1a2a54476", 3}} {
		err := hub.Publish(t.Context(), api.EventTopicProgress, PublishKey{Project: event.project}, api.ProgressEvent{Revision: event.revision})
		testutil.FailErr(t, "publish retained event", err)
	}
	ch, unsubscribe, err := hub.Subscribe(t.Context(), Subscription{Project: "54f6d2ef-89c3-56e9-9a76-f6b1a2a54476", Viewer: testutil.HostOwner(), After: boundary})
	testutil.FailErr(t, "subscribe after boundary", err)
	defer unsubscribe()
	for _, want := range []uint64{1, 3} {
		envelope := testutil.Receive(t, "replayed project event", ch)
		var event api.ProgressEvent
		testutil.FailErr(t, "decode replay", json.Unmarshal(envelope.Data, &event))
		if event.Revision != want || envelope.Cursor == "" {
			t.Fatalf("replay = revision %d cursor %q", event.Revision, envelope.Cursor)
		}
	}
	testutil.FailErr(t, "publish live event", hub.Publish(t.Context(), api.EventTopicProgress, PublishKey{Project: "54f6d2ef-89c3-56e9-9a76-f6b1a2a54476"}, api.ProgressEvent{Revision: 4}))
	var live api.ProgressEvent
	testutil.FailErr(t, "decode live event", json.Unmarshal(testutil.Receive(t, "live project event", ch).Data, &live))
	if live.Revision != 4 {
		t.Fatalf("live revision = %d", live.Revision)
	}
}

func TestSubscribeRejectsExpiredForeignAndTamperedCursors(t *testing.T) {
	hub := NewMemoryHub()
	expired := hub.CurrentCursor()
	for i := 0; i <= replayCapacity; i++ {
		testutil.FailErr(t, "publish retained event", hub.Publish(t.Context(), api.EventTopicProgress, PublishKey{Project: "54f6d2ef-89c3-56e9-9a76-f6b1a2a54476"}, api.ProgressEvent{Revision: uint64(i + 1)}))
	}
	foreign := NewMemoryHub().CurrentCursor()
	current := hub.CurrentCursor()
	replacement := byte('A')
	if current[0] == replacement {
		replacement = 'B'
	}
	tampered := string(replacement) + current[1:]
	for name, cursor := range map[string]string{"expired": expired, "foreign": foreign, "tampered": tampered} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := hub.Subscribe(t.Context(), Subscription{Project: "54f6d2ef-89c3-56e9-9a76-f6b1a2a54476", Viewer: testutil.HostOwner(), After: cursor}); !errors.Is(err, ErrReplayUnavailable) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestHubDebounceBoard(t *testing.T) {
	hub := NewMemoryHub()
	ctx := context.Background()

	ch, unsubscribe, err := hub.Subscribe(ctx, Subscription{Project: "54f6d2ef-89c3-56e9-9a76-f6b1a2a54476", Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "hub.Subscribe failed", err)
	defer unsubscribe()

	for i := 0; i < 3; i++ {
		_ = hub.Publish(ctx, api.EventTopicBoard, PublishKey{Project: "54f6d2ef-89c3-56e9-9a76-f6b1a2a54476"}, api.BoardEvent{ProjectID: "54f6d2ef-89c3-56e9-9a76-f6b1a2a54476"})
	}
	hub.FlushDebounced()

	select {
	case envelope := <-ch:
		if envelope.Topic != api.EventTopicBoard {
			t.Fatalf("topic=%q want board", envelope.Topic)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for debounced board event")
	}

	select {
	case <-ch:
		t.Fatal("expected no more board events")
	default:
	}
}

func TestHubNoDebounceWorker(t *testing.T) {
	hub := NewMemoryHub()
	ctx := context.Background()

	ch, unsubscribe, err := hub.Subscribe(ctx, Subscription{Project: "54f6d2ef-89c3-56e9-9a76-f6b1a2a54476", Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "hub.Subscribe failed", err)
	defer unsubscribe()

	for i := 0; i < 3; i++ {
		_ = hub.Publish(ctx, api.EventTopicWorker, PublishKey{Project: "54f6d2ef-89c3-56e9-9a76-f6b1a2a54476"}, api.WorkerEvent{
			WorkerID: fmt.Sprintf("task-%d", i),
		})
	}

	for i := 0; i < 3; i++ {
		select {
		case envelope := <-ch:
			if envelope.Topic != api.EventTopicWorker {
				t.Fatalf("topic=%q want worker", envelope.Topic)
			}
		case <-time.After(time.Second):
			t.Fatalf("timeout waiting for worker event %d", i)
		}
	}
}

func TestHubUnsubscribe(t *testing.T) {
	hub := NewMemoryHub()
	ctx := context.Background()

	ch, unsubscribe, err := hub.Subscribe(ctx, Subscription{Project: "54f6d2ef-89c3-56e9-9a76-f6b1a2a54476", Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "hub.Subscribe failed", err)
	unsubscribe()

	_ = hub.Publish(ctx, api.EventTopicSession, PublishKey{Project: "54f6d2ef-89c3-56e9-9a76-f6b1a2a54476"}, api.SessionEvent{
		ID: "session-1", ProjectID: "54f6d2ef-89c3-56e9-9a76-f6b1a2a54476", Action: api.SessionEventActionUpdated, Status: api.SessionStatusIdle,
	})
	hub.FlushDebounced()

	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("expected no events after unsubscribe")
		}
	default:
	}
}

func TestHubDropsSubscriberWhoseBufferFills(t *testing.T) {
	hub := NewMemoryHub()
	ctx := context.Background()

	ch, unsubscribe, err := hub.Subscribe(ctx, Subscription{Project: "54f6d2ef-89c3-56e9-9a76-f6b1a2a54476", Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "hub.Subscribe failed", err)
	defer unsubscribe()

	// Fill the subscriber without draining it.
	for i := 0; i < SubscriberBufferSize+1; i++ {
		_ = hub.Publish(ctx, api.EventTopicMessage, PublishKey{Project: "54f6d2ef-89c3-56e9-9a76-f6b1a2a54476"},
			api.MessageEvent{SessionID: "session-1"})
	}

	// Closing the channel forces a reconnect.
	if got := hub.SubscriberCount(); got != 0 {
		t.Fatalf("SubscriberCount = %d, want the overflowing subscriber dropped", got)
	}
	for i := 0; i < SubscriberBufferSize; i++ {
		if _, ok := <-ch; !ok {
			t.Fatalf("buffered event %d missing before close", i)
		}
	}
	if _, ok := <-ch; ok {
		t.Fatal("channel should be closed once the subscriber is dropped")
	}
}

func TestHubUnsubscribeAfterDropIsSafe(t *testing.T) {
	hub := NewMemoryHub()
	ctx := context.Background()

	_, unsubscribe, err := hub.Subscribe(ctx, Subscription{Project: "54f6d2ef-89c3-56e9-9a76-f6b1a2a54476", Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "hub.Subscribe failed", err)

	for i := 0; i < SubscriberBufferSize+1; i++ {
		_ = hub.Publish(ctx, api.EventTopicMessage, PublishKey{Project: "54f6d2ef-89c3-56e9-9a76-f6b1a2a54476"},
			api.MessageEvent{SessionID: "session-1"})
	}

	// Deferred cleanup may follow overflow cleanup.
	unsubscribe()
	unsubscribe()
}

func TestHubProjectIsolation(t *testing.T) {
	hub := NewMemoryHub()
	ctx := context.Background()

	ch, unsubscribe, err := hub.Subscribe(ctx, Subscription{Project: "54f6d2ef-89c3-56e9-9a76-f6b1a2a54476", Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "hub.Subscribe failed", err)
	defer unsubscribe()

	_ = hub.Publish(ctx, api.EventTopicSession, PublishKey{Project: "4e462c43-fa37-57eb-a684-39941434b792"}, api.SessionEvent{
		ID: "session-2", ProjectID: "4e462c43-fa37-57eb-a684-39941434b792", Action: api.SessionEventActionUpdated, Status: api.SessionStatusIdle,
	})
	hub.FlushDebounced()

	select {
	case <-ch:
		t.Fatal("expected no events for different project")
	default:
	}
}

func TestHubMultipleSubscribers(t *testing.T) {
	hub := NewMemoryHub()
	ctx := context.Background()

	ch1, unsub1, err := hub.Subscribe(ctx, Subscription{Project: "54f6d2ef-89c3-56e9-9a76-f6b1a2a54476", Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "hub.Subscribe failed", err)
	defer unsub1()
	ch2, unsub2, err := hub.Subscribe(ctx, Subscription{Project: "54f6d2ef-89c3-56e9-9a76-f6b1a2a54476", Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "hub.Subscribe failed", err)
	defer unsub2()

	_ = hub.Publish(ctx, api.EventTopicSession, PublishKey{Project: "54f6d2ef-89c3-56e9-9a76-f6b1a2a54476"}, api.SessionEvent{
		ID: "session-1", ProjectID: "54f6d2ef-89c3-56e9-9a76-f6b1a2a54476", Action: api.SessionEventActionUpdated, Status: api.SessionStatusIdle,
	})
	hub.FlushDebounced()

	select {
	case <-ch1:
	case <-time.After(time.Second):
		t.Fatal("subscriber 1 timeout")
	}
	select {
	case <-ch2:
	case <-time.After(time.Second):
		t.Fatal("subscriber 2 timeout")
	}
}

func TestHubDebounceLLM(t *testing.T) {
	hub := NewMemoryHub()
	ctx := context.Background()

	ch, unsubscribe, err := hub.Subscribe(ctx, Subscription{Project: "54f6d2ef-89c3-56e9-9a76-f6b1a2a54476", Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "hub.Subscribe failed", err)
	defer unsubscribe()

	for i := 0; i < 2; i++ {
		_ = hub.Publish(ctx, api.EventTopicLLM, PublishKey{Project: "54f6d2ef-89c3-56e9-9a76-f6b1a2a54476", Session: "s1"}, api.LLMCallEvent{
			CallID: fmt.Sprintf("call-%d", i),
			Status: api.LLMCallStatusOK,
		})
	}
	hub.FlushDebounced()

	select {
	case envelope := <-ch:
		if envelope.Topic != api.EventTopicLLM {
			t.Fatalf("topic=%q want llm", envelope.Topic)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for debounced llm event")
	}

	select {
	case <-ch:
		t.Fatal("expected single debounced llm event")
	default:
	}
}

func TestSubscribeRequiresAViewer(t *testing.T) {
	hub := NewMemoryHub()
	if _, _, err := hub.Subscribe(t.Context(), Subscription{}); !errors.Is(err, ErrSubscriptionViewer) {
		t.Fatalf("anonymous subscription err=%v want ErrSubscriptionViewer", err)
	}
}

func TestViewerWithoutEventAuthorityReceivesNothing(t *testing.T) {
	hub := NewMemoryHub()
	ctx := t.Context()
	stranger, stopStranger, err := hub.Subscribe(ctx, Subscription{Viewer: people.Person{ID: "person-1", Role: api.PersonRole("unrecognized")}})
	testutil.FailErr(t, "subscribe stranger", err)
	defer stopStranger()
	owner, stopOwner, err := hub.Subscribe(ctx, Subscription{Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "subscribe owner", err)
	defer stopOwner()

	testutil.FailErr(t, "publish", hub.Publish(ctx, api.EventTopicSettings, PublishKey{}, api.SettingsEvent{}))
	hub.FlushDebounced()

	select {
	case <-owner:
	case <-time.After(time.Second):
		t.Fatal("owner did not receive the event")
	}
	select {
	case env := <-stranger:
		t.Fatalf("viewer without authority received %s", env.Topic)
	case <-time.After(50 * time.Millisecond):
	}
}
