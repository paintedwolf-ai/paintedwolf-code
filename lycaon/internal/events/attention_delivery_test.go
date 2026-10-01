package events

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type delayedAttentionHub struct {
	EventHub
	entered chan struct{}
	release chan struct{}
}

func (h delayedAttentionHub) Publish(ctx context.Context, topic api.EventTopic, key PublishKey, data any) error {
	view, ok := data.(api.AttentionView)
	if ok && len(view.Rows) > 0 && view.Rows[0].SessionID == "stale" {
		close(h.entered)
		<-h.release
	}
	return h.EventHub.Publish(ctx, topic, key, data)
}

type sequencedAttention struct{ calls atomic.Int32 }

func (s *sequencedAttention) BuildView(context.Context) (api.AttentionView, error) {
	id := "fresh"
	if s.calls.Add(1) == 1 {
		id = "stale"
	}
	return api.AttentionView{Rows: []api.AttentionRow{{SessionID: id}}}, nil
}

func TestAttentionDeliveryCannotReorderAcceptedViews(t *testing.T) {
	hub := delayedAttentionHub{EventHub: NewMemoryHub(), entered: make(chan struct{}), release: make(chan struct{})}
	stream, unsubscribe, err := hub.Subscribe(t.Context(), Subscription{Project: "", Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "subscribe to attention", err)
	defer unsubscribe()
	pub := &Publisher{Hub: hub, Attention: &sequencedAttention{}}
	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		pub.FlushAttention(t.Context())
	}()
	<-hub.entered
	secondDone := make(chan struct{})
	go func() {
		defer close(secondDone)
		pub.FlushAttention(t.Context())
	}()
	select {
	case <-secondDone:
		t.Error("newer attention bypassed a pending publication")
	case <-time.After(100 * time.Millisecond):
	}
	close(hub.release)
	<-firstDone
	<-secondDone
	for _, want := range []string{"stale", "fresh"} {
		envelope := expectTopic(t, stream, api.EventTopicAttention)
		var view api.AttentionView
		testutil.FailErr(t, "decode attention", json.Unmarshal(envelope.Data, &view))
		if len(view.Rows) != 1 || view.Rows[0].SessionID != want {
			t.Fatalf("attention = %+v, want %s", view.Rows, want)
		}
	}
}
