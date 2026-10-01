package stream

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// Deferred projection persists after the turn is canceled.
func TestScheduleLiveProjectionSurvivesCallerContextCancellation(t *testing.T) {
	mgr := New(nil)
	mem := store.NewMemory()
	ctx := context.Background()
	sess, err := mem.Create(ctx, api.CreateSessionRequest{}, "proj-1")
	testutil.FailErr(t, "Create session", err)
	testutil.FailErr(t, "AppendMessages", mem.AppendMessages(ctx, sess.ID, api.Message{
		ID:   "msg-live",
		Role: api.MessageRoleAssistant,
	}))
	mgr.store = mem

	turnCtx, cancel := context.WithCancel(ctx)
	mgr.Schedule(turnCtx, sess.ID, api.Message{ID: "msg-live", Content: "partial before stop"})
	// Simulate Stop canceling the in-flight turn's context inside the debounce window.
	cancel()

	testutil.WaitFor(t, time.Second, func() bool {
		msg, err := mem.GetMessage(context.Background(), sess.ID, "msg-live")
		return err == nil && msg.Content == "partial before stop"
	})
}

func TestLiveStreamFansContentAndDone(t *testing.T) {
	mgr := New(nil)
	ctx := context.Background()
	msgID := "live-1"
	sessionID := "sess-1"

	ch, unsub := mgr.Subscribe(msgID)
	t.Cleanup(unsub)

	mgr.SetActive(sessionID, msgID, 1)
	testutil.FailErr(t, "project live", mgr.Project(ctx, sessionID, api.Message{
		ID:      msgID,
		Role:    api.MessageRoleAssistant,
		Content: "hello world",
	}))
	mgr.Flush(ctx, sessionID)

	select {
	case frame := <-ch:
		if frame.Content != "hello world" {
			t.Fatalf("frame = %+v", frame)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for live stream frame")
	}

	mgr.Finish(ctx, sessionID)
	if active := mgr.ActiveMessageID(sessionID); active != "" {
		t.Fatalf("active message after clear = %q", active)
	}

	select {
	case frame := <-ch:
		if !frame.Done {
			t.Fatalf("expected Done, got %+v", frame)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for Done")
	}
}

func TestLiveStreamDoneDisplacesStaleContentWithLatestSnapshot(t *testing.T) {
	mgr := New(nil)
	sessionID := "sess-slow"
	messageID := "live-slow"
	ch, unsub := mgr.Subscribe(messageID)
	t.Cleanup(unsub)
	mgr.SetActive(sessionID, messageID, 1)

	hub := mgr.hub
	for i := 0; i < cap(ch); i++ {
		hub.mu.Lock()
		hub.deliver(messageID, Frame{Content: fmt.Sprintf("snapshot-%d", i)})
		hub.mu.Unlock()
	}
	hub.publish(api.Message{ID: messageID, Content: "latest snapshot"})
	mgr.signalDone(sessionID, messageID)

	latest := <-ch
	if latest.Done || latest.Content != "latest snapshot" {
		t.Fatalf("final content frame = %+v, want latest snapshot", latest)
	}
	if done := <-ch; !done.Done {
		t.Fatalf("terminal frame = %+v, want done", done)
	}
}

func TestLiveStreamClearFlushesLatestProjectionBeforeDone(t *testing.T) {
	mgr := New(nil)
	ctx := context.Background()
	msgID := "live-final"
	sessionID := "sess-final"

	ch, unsub := mgr.Subscribe(msgID)
	t.Cleanup(unsub)
	mgr.SetActive(sessionID, msgID, 1)
	testutil.FailErr(t, "project final live frame", mgr.Project(ctx, sessionID, api.Message{
		ID:      msgID,
		Role:    api.MessageRoleAssistant,
		Content: "complete answer",
	}))

	mgr.Finish(ctx, sessionID)

	content := <-ch
	if content.Content != "complete answer" || content.Done {
		t.Fatalf("content frame = %+v", content)
	}
	done := <-ch
	if !done.Done {
		t.Fatalf("terminal frame = %+v", done)
	}
}

func TestLiveStreamStopsAdvertisingActiveBeforeTerminalFanout(t *testing.T) {
	mgr := New(nil)
	sessionID := "sess-finishing"
	messageID := "live-finishing"
	mgr.SetActive(sessionID, messageID, 1)

	hub := mgr.hub
	hub.flushMu.Lock()
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		mgr.Finish(t.Context(), sessionID)
	}()

	deadline := time.After(time.Second)
	for mgr.ActiveMessageID(sessionID) != "" {
		select {
		case <-deadline:
			hub.flushMu.Unlock()
			t.Fatal("stream stayed active while terminal fanout was blocked")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	hub.flushMu.Unlock()
	<-finished
}

func TestLiveStreamUnsubscribeIsIdempotent(t *testing.T) {
	mgr := New(nil)
	_, unsub := mgr.Subscribe("live-idempotent")
	unsub()
	unsub()
}

func TestLiveStreamSubscriptionStartsAtLatestPublishedSnapshot(t *testing.T) {
	mgr := New(nil)
	hub := mgr.hub
	hub.publish(api.Message{ID: "message", Content: "published"})

	ch, unsub := mgr.Subscribe("message")
	t.Cleanup(unsub)
	if frame := <-ch; frame.Content != "published" {
		t.Fatalf("initial frame = %+v", frame)
	}
}

func TestLiveStreamFlushesRemainOrdered(t *testing.T) {
	mgr := New(nil)
	hub := mgr.hub
	ch, unsub := mgr.Subscribe("message")
	t.Cleanup(unsub)
	hub.mu.Lock()
	hub.pending["message"] = livePending{msg: api.Message{ID: "message", Content: "first"}}
	hub.mu.Unlock()

	started := make(chan struct{})
	release := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		hub.flush(t.Context(), "message", func(context.Context, string, api.Message) error {
			close(started)
			<-release
			return nil
		})
	}()
	<-started
	hub.mu.Lock()
	hub.pending["message"] = livePending{msg: api.Message{ID: "message", Content: "second"}}
	hub.mu.Unlock()
	go func() {
		defer wg.Done()
		hub.flush(t.Context(), "message", nil)
	}()
	close(release)
	wg.Wait()

	if first, second := <-ch, <-ch; first.Content != "first" || second.Content != "second" {
		t.Fatalf("frames = %+v, %+v", first, second)
	}
}

func TestLiveStreamDoneAndUnsubscribeAreSynchronized(t *testing.T) {
	const attempts = 1_000
	for i := 0; i < attempts; i++ {
		mgr := New(nil)
		sessionID := fmt.Sprintf("session-%d", i)
		messageID := fmt.Sprintf("message-%d", i)
		_, unsub := mgr.Subscribe(messageID)
		mgr.SetActive(sessionID, messageID, 1)

		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			mgr.signalDone(sessionID, messageID)
		}()
		go func() {
			defer wg.Done()
			<-start
			unsub()
		}()
		close(start)
		wg.Wait()
	}
}
