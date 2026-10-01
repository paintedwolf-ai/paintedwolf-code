package security

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
)

// Subscriber overflow closes the stream; stored messages supply recovery.

const overflowSessionMessages = events.SubscriberBufferSize + 64

func TestSSESubscriberOverflowIsFullyRecoverableFromStore(t *testing.T) {
	h := wiring.BuildForTest(t, wiring.WithoutCoordinatorLoop())
	ctx := context.Background()

	dir := h.ProjectDir(t, "sse-overflow")
	sess := createSessionHTTP(t, h.Server, dir)
	proj := getSessionHTTP(t, h.Server, sess.ID).ProjectID

	// Leaving the subscriber undrained simulates a stalled client.
	stream, unsubscribe, err := h.Events.Subscribe(ctx, events.Subscription{Project: proj, Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "subscribe", err)
	defer unsubscribe()

	// Messages are durable before their events are published.
	want := make([]string, 0, overflowSessionMessages)
	for i := range overflowSessionMessages {
		msg := wire.Message{
			ID:      uuid.NewString(),
			Role:    wire.MessageRoleAssistant,
			Content: fmt.Sprintf("overflow transcript entry %d", i),
			Origin:  wire.MessageOriginModel,
		}
		testutil.FailErr(t, "append message",
			h.Store.AppendMessages(ctx, sess.ID, msg))
		testutil.FailErr(t, "publish append",
			h.Events.Publish(ctx, wire.EventTopicMessage,
				events.PublishKey{Project: proj, Session: sess.ID},
				wire.MessageEvent{
					SessionID: sess.ID,
					Op:        wire.MessageChangeAppend,
					Message:   msg,
				}))
		want = append(want, msg.ID)
	}

	// Recovery is exercised only after a confirmed overflow.
	delivered := drainUntilClosed(t, stream, 10*time.Second)
	if delivered >= overflowSessionMessages {
		t.Fatalf("subscriber received all %d appends without being dropped — "+
			"buffer size or publish path changed; this test needs a real overflow",
			delivered)
	}
	if delivered == 0 {
		t.Fatal("subscriber received nothing — the burst never reached deliver")
	}
	t.Logf("subscriber dropped after %d/%d appends", delivered, overflowSessionMessages)

	// Recovery follows before cursors to include messages outside the newest page.
	got := pageWholeTranscript(t, h.Server, sess.ID)
	byID := make(map[string]wire.Message, len(got))
	for _, m := range got {
		byID[m.ID] = m
	}
	missing := 0
	for i, id := range want {
		if _, ok := byID[id]; !ok {
			if missing < 5 {
				t.Errorf("message %d (%s) absent after resync", i, id)
			}
			missing++
		}
	}
	if missing > 0 {
		t.Fatalf("%d/%d appends unrecoverable after subscriber drop — dropping a "+
			"subscriber is only safe if the store is complete", missing, len(want))
	}

	assertTranscriptOrder(t, got, want)

	resub, resubClose, err := h.Events.Subscribe(ctx, events.Subscription{Project: proj, Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "resubscribe", err)
	defer resubClose()

	after := wire.Message{
		ID:      uuid.NewString(),
		Role:    wire.MessageRoleAssistant,
		Content: "post-reconnect entry",
		Origin:  wire.MessageOriginModel,
	}
	testutil.FailErr(t, "append post-reconnect",
		h.Store.AppendMessages(ctx, sess.ID, after))
	testutil.FailErr(t, "publish post-reconnect",
		h.Events.Publish(ctx, wire.EventTopicMessage,
			events.PublishKey{Project: proj, Session: sess.ID},
			wire.MessageEvent{
				SessionID: sess.ID,
				Op:        wire.MessageChangeAppend,
				Message:   after,
			}))

	select {
	case env, ok := <-resub:
		if !ok {
			t.Fatal("reconnected subscriber closed immediately")
		}
		if env.Topic != wire.EventTopicMessage {
			t.Fatalf("reconnected subscriber topic = %q want message", env.Topic)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("reconnected subscriber received nothing — the drop poisoned the project stream")
	}
}

// Prepending older pages preserves ascending transcript order.
func pageWholeTranscript(t *testing.T, srv *api.Server, sessionID string) []wire.Message {
	t.Helper()
	const window = 500 // endpoint maximum; fewer round trips, same cursor logic
	var all []wire.Message
	before := ""
	for pages := 0; ; pages++ {
		if pages > 64 {
			t.Fatal("transcript paging did not terminate — cursor is not advancing")
		}
		path := fmt.Sprintf("/v1/sessions/%s/messages?limit=%d", sessionID, window)
		if before != "" {
			path += "&before=" + before
		}
		req := authedRequest(t, http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("transcript page status = %d body = %s", w.Code, w.Body.String())
		}
		var page wire.SessionTranscriptPage
		if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
			t.Fatalf("decode transcript page: %v", err)
		}
		all = append(page.Messages, all...)
		if page.BeforeCursor == "" {
			return all
		}
		before = page.BeforeCursor
	}
}

// Buffered events remain readable after the hub closes a dropped subscriber.
func drainUntilClosed(t *testing.T, ch <-chan wire.EventEnvelope, timeout time.Duration) int {
	t.Helper()
	count := 0
	timeout = testutil.Timeout(timeout)
	deadline := time.After(timeout)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return count
			}
			count++
		case <-deadline:
			t.Fatalf("subscriber still open after %s and %d events — expected the hub to drop it",
				timeout, count)
			return count
		}
	}
}

// Harness-generated rows can appear between the expected messages.
func assertTranscriptOrder(t *testing.T, got []wire.Message, want []string) {
	t.Helper()
	position := make(map[string]int, len(got))
	for i, m := range got {
		position[m.ID] = i
	}
	prev := -1
	for i, id := range want {
		at, ok := position[id]
		if !ok {
			continue // presence is asserted by the caller
		}
		if at <= prev {
			t.Fatalf("append %d (%s) resynced out of order: index %d after %d", i, id, at, prev)
		}
		prev = at
	}
}

func TestSSEResyncTranscriptEndpointRequiresAuth(t *testing.T) {
	h := wiring.BuildForTest(t, wiring.WithoutCoordinatorLoop())
	dir := h.ProjectDir(t, "sse-overflow-auth")
	sess := createSessionHTTP(t, h.Server, dir)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet,
		"/v1/sessions/"+sess.ID+"/messages", nil)
	w := httptest.NewRecorder()
	h.Server.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated resync status = %d want 401 — the recovery path "+
			"must not become an unauthenticated transcript read", w.Code)
	}
}
