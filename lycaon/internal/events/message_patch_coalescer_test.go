package events

import (
	"context"
	"encoding/json"
	"testing"
	"testing/synctest"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestMessagePatchCoalescerCoalescesBursts(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		hub := NewMemoryHub()
		pub := &Publisher{Hub: hub}
		ctx := context.Background()

		ch, unsub, err := hub.Subscribe(ctx, Subscription{Project: "84a676a3-ccd2-5967-97ed-fd384c0b9003", Viewer: testutil.HostOwner()})
		testutil.FailErr(t, "hub.Subscribe failed", err)
		defer unsub()

		key := PublishKey{Project: "84a676a3-ccd2-5967-97ed-fd384c0b9003", Session: "sess-1"}
		for i := 0; i < 5; i++ {
			pub.PublishMessagePatch(ctx, key.Project, key.Session, api.Message{
				ID:      "msg-1",
				Role:    api.MessageRoleAssistant,
				Content: "partial",
			})
		}

		select {
		case envelope := <-ch:
			if envelope.Topic != api.EventTopicMessage {
				t.Fatalf("topic = %q want message", envelope.Topic)
			}
			var ev api.MessageEvent
			if err := json.Unmarshal(envelope.Data, &ev); err != nil {
				testutil.FailErr(t, "unmarshal JSON document", err)
			}
			if ev.Message.Content != "partial" {
				t.Fatalf("content = %q", ev.Message.Content)
			}
		case <-time.After(time.Second):
			t.Fatal("timeout waiting for coalesced patch")
		}

		select {
		case envelope := <-ch:
			t.Fatalf("unexpected extra event: %+v", envelope)
		case <-time.After(2 * DebounceMessagePatch):
		}
	})
}

func TestMessagePatchCoalescerSurvivesCallerContextCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		hub := NewMemoryHub()
		pub := &Publisher{Hub: hub}

		ch, unsub, err := hub.Subscribe(context.Background(), Subscription{Project: "84a676a3-ccd2-5967-97ed-fd384c0b9003", Viewer: testutil.HostOwner()})
		testutil.FailErr(t, "hub.Subscribe failed", err)
		defer unsub()

		turnCtx, cancel := context.WithCancel(context.Background())
		pub.PublishMessagePatch(turnCtx, "84a676a3-ccd2-5967-97ed-fd384c0b9003", "sess-1", api.Message{
			ID:      "msg-1",
			Role:    api.MessageRoleAssistant,
			Content: "partial before stop",
		})
		// Cancel before the debounce fires.
		cancel()

		select {
		case envelope := <-ch:
			if envelope.Topic != api.EventTopicMessage {
				t.Fatalf("topic = %q want message", envelope.Topic)
			}
			var ev api.MessageEvent
			if err := json.Unmarshal(envelope.Data, &ev); err != nil {
				testutil.FailErr(t, "unmarshal JSON document", err)
			}
			if ev.Message.Content != "partial before stop" {
				t.Fatalf("content = %q", ev.Message.Content)
			}
		case <-time.After(2 * DebounceMessagePatch):
			t.Fatal("debounced patch was dropped after caller context cancellation")
		}
	})
}

func TestPublishSessionIdleFlushesPendingMessagePatchesFirst(t *testing.T) {
	hub := NewMemoryHub()
	pub := &Publisher{Hub: hub}
	ctx := context.Background()

	ch, unsub, err := hub.Subscribe(ctx, Subscription{Project: "84a676a3-ccd2-5967-97ed-fd384c0b9003", Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "hub.Subscribe failed", err)
	defer unsub()

	pub.PublishMessagePatch(ctx, "84a676a3-ccd2-5967-97ed-fd384c0b9003", "sess-1", api.Message{
		ID:      "msg-1",
		Role:    api.MessageRoleAssistant,
		Content: "final streaming chunk",
	})
	pub.PublishSession(ctx, "84a676a3-ccd2-5967-97ed-fd384c0b9003", "sess-1", api.SessionStatusIdle, "preview")

	first := testutil.Receive(t, "message patch before idle", ch)
	if first.Topic != api.EventTopicMessage {
		t.Fatalf("first envelope topic = %q want message (idle must trail)", first.Topic)
	}
	var msg api.MessageEvent
	if err := json.Unmarshal(first.Data, &msg); err != nil {
		testutil.FailErr(t, "unmarshal message envelope", err)
	}
	if msg.Message.Content != "final streaming chunk" {
		t.Fatalf("message content = %q want final streaming chunk", msg.Message.Content)
	}

	second := testutil.Receive(t, "idle event after message patch", ch)
	if second.Topic != api.EventTopicSession {
		t.Fatalf("second envelope topic = %q want session", second.Topic)
	}
}

func TestPublishSessionHostErrorFlushesPendingMessagePatchesFirst(t *testing.T) {
	hub := NewMemoryHub()
	pub := &Publisher{Hub: hub}
	ctx := context.Background()

	ch, unsub, err := hub.Subscribe(ctx, Subscription{Project: "84a676a3-ccd2-5967-97ed-fd384c0b9003", Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "hub.Subscribe failed", err)
	defer unsub()

	pub.PublishMessagePatch(ctx, "84a676a3-ccd2-5967-97ed-fd384c0b9003", "sess-1", api.Message{
		ID:      "msg-1",
		Role:    api.MessageRoleAssistant,
		Content: "trailing chunk before host error",
	})
	pub.PublishSessionHostError(ctx, "84a676a3-ccd2-5967-97ed-fd384c0b9003", "sess-1", api.SessionHostError{Code: "TEST_HOST_ERROR"})

	first := testutil.Receive(t, "message patch before host error", ch)
	if first.Topic != api.EventTopicMessage {
		t.Fatalf("first envelope topic = %q want message (host_error must trail)", first.Topic)
	}
	second := testutil.Receive(t, "host-error event after message patch", ch)
	if second.Topic != api.EventTopicSession {
		t.Fatalf("second envelope topic = %q want session", second.Topic)
	}
}

func TestCoalescerFlushSessionLeavesOtherSessionsPending(t *testing.T) {
	hub := NewMemoryHub()
	pub := &Publisher{Hub: hub}
	ctx := context.Background()

	pub.PublishMessagePatch(ctx, "84a676a3-ccd2-5967-97ed-fd384c0b9003", "sess-1", api.Message{
		ID: "msg-1", Role: api.MessageRoleAssistant, Content: "sess-1 pending",
	})
	pub.PublishMessagePatch(ctx, "84a676a3-ccd2-5967-97ed-fd384c0b9003", "sess-other", api.Message{
		ID: "msg-other", Role: api.MessageRoleAssistant, Content: "sess-other pending",
	})

	c := pub.messagePatchCoalescer()
	if c == nil {
		t.Fatal("coalescer not initialized")
	}
	c.flushSession(ctx, "sess-1")

	c.mu.Lock()
	_, sess1Pending := c.pending[messagePatchKey(PublishKey{Project: "84a676a3-ccd2-5967-97ed-fd384c0b9003", Session: "sess-1"}, "msg-1")]
	_, otherPending := c.pending[messagePatchKey(PublishKey{Project: "84a676a3-ccd2-5967-97ed-fd384c0b9003", Session: "sess-other"}, "msg-other")]
	c.mu.Unlock()

	if sess1Pending {
		t.Fatal("sess-1 patch still pending after flushSession(sess-1)")
	}
	if !otherPending {
		t.Fatal("sess-other patch was drained by flushSession(sess-1) — must remain coalescing")
	}
}
