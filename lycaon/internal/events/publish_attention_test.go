package events

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type stubAttention struct {
	view  api.AttentionView
	calls int
}

func (s *stubAttention) BuildView(context.Context) (api.AttentionView, error) {
	s.calls++
	return s.view, nil
}

// Attention crosses project subscriptions so blocked work remains visible.
func TestAttentionReachesSubscriberOfAnotherProject(t *testing.T) {
	hub := NewMemoryHub()
	src := &stubAttention{view: api.AttentionView{Rows: []api.AttentionRow{{
		SessionID: "s1",
		ProjectID: "59fe5320-05b6-55f5-8365-760a4247b8c9",
		Class:     api.AttentionClassNeedsYou,
		Reason:    api.AttentionReasonCheckpoint,
	}}}}
	pub := &Publisher{Hub: hub, Attention: src}

	ch, unsubscribe, err := hub.Subscribe(context.Background(), Subscription{Project: "84a676a3-ccd2-5967-97ed-fd384c0b9003", Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "hub.Subscribe", err)
	t.Cleanup(unsubscribe)

	pub.FlushAttention(context.Background())

	select {
	case env := <-ch:
		if env.Topic != api.EventTopicAttention {
			t.Fatalf("want attention topic, got %q", env.Topic)
		}
		var view api.AttentionView
		testutil.FailErr(t, "unmarshal", json.Unmarshal(env.Data, &view))
		if len(view.Rows) != 1 || view.Rows[0].ProjectID != "59fe5320-05b6-55f5-8365-760a4247b8c9" {
			t.Fatalf("want the other project's row, got %+v", view.Rows)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("attention event never reached a subscriber of a different project")
	}
}

// Debouncing limits cross-project rebuilds during streaming.
func TestPublishAttentionCoalescesRebuilds(t *testing.T) {
	hub := NewMemoryHub()
	src := &stubAttention{}
	pub := &Publisher{Hub: hub, Attention: src}

	for range 25 {
		pub.PublishAttention(context.Background())
	}
	if src.calls != 0 {
		t.Fatalf("no rebuild should happen before the window elapses, got %d", src.calls)
	}
	pub.FlushAttention(context.Background())
	if src.calls != 1 {
		t.Fatalf("want a single coalesced rebuild, got %d", src.calls)
	}
}

// The scheduled rebuild outlives its triggering turn.
func TestCoalescedRebuildSurvivesTriggerContextCancel(t *testing.T) {
	hub := NewMemoryHub()
	src := &stubAttention{}
	pub := &Publisher{Hub: hub, Attention: src}

	ch, unsubscribe, err := hub.Subscribe(context.Background(), Subscription{Project: "", Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "hub.Subscribe", err)
	t.Cleanup(unsubscribe)

	ctx, cancel := context.WithCancel(context.Background())
	pub.PublishAttention(ctx)
	cancel()

	select {
	case env := <-ch:
		if env.Topic != api.EventTopicAttention {
			t.Fatalf("want attention topic, got %q", env.Topic)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("rebuild scheduled before cancel never published")
	}
}

// blockingAttention holds one build open while another completes.
type blockingAttention struct {
	release chan struct{}
	entered chan struct{}
}

func (s *blockingAttention) BuildView(context.Context) (api.AttentionView, error) {
	s.entered <- struct{}{}
	<-s.release
	return api.AttentionView{Rows: []api.AttentionRow{{SessionID: "stale"}}}, nil
}

type staticAttention struct{ sessionID string }

func (s staticAttention) BuildView(context.Context) (api.AttentionView, error) {
	return api.AttentionView{Rows: []api.AttentionRow{{SessionID: s.sessionID}}}, nil
}

// A delayed build cannot replace a newer published revision.
func TestPublishAttentionOrdersConcurrentBuildsByStartNotFinish(t *testing.T) {
	hub := NewMemoryHub()
	slow := &blockingAttention{release: make(chan struct{}), entered: make(chan struct{})}
	pub := &Publisher{Hub: hub, Attention: slow}
	ctx := context.Background()

	ch, unsubscribe, err := hub.Subscribe(ctx, Subscription{Project: "", Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "hub.Subscribe", err)
	t.Cleanup(unsubscribe)

	// Hold the first build after its timer releases the debounce slot.
	pub.PublishAttention(ctx)
	<-slow.entered

	// Flush the newer revision while the first build remains blocked.
	pub.Attention = staticAttention{sessionID: "fresh"}
	pub.FlushAttention(ctx)

	fresh := expectTopic(t, ch, api.EventTopicAttention)
	var freshView api.AttentionView
	testutil.FailErr(t, "unmarshal", json.Unmarshal(fresh.Data, &freshView))
	if len(freshView.Rows) != 1 || freshView.Rows[0].SessionID != "fresh" {
		t.Fatalf("first delivered view = %+v, want fresh", freshView.Rows)
	}

	// Release the stale build after the fresh revision publishes.
	close(slow.release)

	select {
	case env := <-ch:
		var staleView api.AttentionView
		testutil.FailErr(t, "unmarshal", json.Unmarshal(env.Data, &staleView))
		t.Fatalf("stale late build must not publish, got %+v", staleView.Rows)
	case <-time.After(200 * time.Millisecond):
		// The stale build is discarded.
	}
}

func TestPublishAttentionNoopWithoutSource(t *testing.T) {
	pub := &Publisher{Hub: NewMemoryHub()}
	pub.PublishAttention(context.Background())
	pub.FlushAttention(context.Background())
}

func TestAttentionLifecycleTopic(t *testing.T) {
	if !AttentionLifecycleTopic(api.EventTopicProject) || !AttentionLifecycleTopic(api.EventTopicSession) {
		t.Fatal("project and session death must rebuild attention")
	}
	if AttentionLifecycleTopic(api.EventTopicAttention) || AttentionLifecycleTopic(api.EventTopicMessage) {
		t.Fatal("attention and message must not loop a rebuild")
	}
}
