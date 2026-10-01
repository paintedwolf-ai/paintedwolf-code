package events

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type blockedAttentionSource struct {
	entered chan struct{}
	release chan struct{}
}

func (s blockedAttentionSource) BuildView(context.Context) (api.AttentionView, error) {
	close(s.entered)
	<-s.release
	return api.AttentionView{}, nil
}

type countedPublishHub struct {
	EventHub
	calls atomic.Int32
}

func (h *countedPublishHub) Publish(context.Context, api.EventTopic, PublishKey, any) error {
	h.calls.Add(1)
	return nil
}

func TestPublisherCloseDrainsRunningAttention(t *testing.T) {
	source := blockedAttentionSource{entered: make(chan struct{}), release: make(chan struct{})}
	hub := &countedPublishHub{}
	p := &Publisher{Hub: hub, Attention: source}
	p.PublishAttention(t.Context())
	select {
	case <-source.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("attention rebuild did not start")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := p.Close(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("close with active work = %v", err)
	}
	close(source.release)
	testutil.FailErr(t, "drain attention", p.Close(t.Context()))
	if hub.calls.Load() != 0 {
		t.Fatal("attention published after close")
	}
	p.PublishAttention(t.Context())
	p.FlushAttention(t.Context())
	testutil.FailErr(t, "close again", p.Close(t.Context()))
}

func TestPublisherCloseDiscardsPendingProjections(t *testing.T) {
	hub := &countedPublishHub{}
	attention := &sequencedAttention{}
	p := &Publisher{Hub: hub, Attention: attention}
	p.PublishAttention(t.Context())
	patches := p.messagePatchCoalescer()
	patch := api.MessageEvent{Message: api.Message{ID: "message"}}
	key := PublishKey{Session: "session"}
	patches.schedule(t.Context(), key, patch)
	testutil.FailErr(t, "close publisher", p.Close(t.Context()))
	// A retained coalescer also rejects scheduling after close.
	patches.schedule(t.Context(), key, patch)
	patches.flushSession(t.Context(), key.Session)
	patches.flush(t.Context(), messagePatchKey(key, patch.Message.ID))
	p.PublishAttention(t.Context())
	p.FlushAttention(t.Context())
	if hub.calls.Load() != 0 || attention.calls.Load() != 0 {
		t.Fatal("closed publisher performed deferred work")
	}
	if p.messagePatchCoalescer() != nil {
		t.Fatal("closed publisher recreated its coalescer")
	}
}

type blockedPatchHub struct {
	EventHub
	entered, release chan struct{}
}

func (h blockedPatchHub) Publish(context.Context, api.EventTopic, PublishKey, any) error {
	close(h.entered)
	<-h.release
	return nil
}

func TestPublisherCloseDrainsRunningPatch(t *testing.T) {
	hub := blockedPatchHub{entered: make(chan struct{}), release: make(chan struct{})}
	p := &Publisher{Hub: hub}
	p.messagePatchCoalescer().schedule(t.Context(), PublishKey{Session: "session"}, api.MessageEvent{Message: api.Message{ID: "message"}})
	select {
	case <-hub.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("patch delivery did not start")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := p.Close(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("close with active delivery = %v", err)
	}
	close(hub.release)
	testutil.FailErr(t, "drain patch", p.Close(t.Context()))
}

func TestPublisherCloseDeadlineCoversAttentionDelivery(t *testing.T) {
	hub := blockedPatchHub{entered: make(chan struct{}), release: make(chan struct{})}
	p := &Publisher{Hub: hub, Attention: &sequencedAttention{}}
	p.PublishAttention(t.Context())
	select {
	case <-hub.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("attention delivery did not start")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	closed := make(chan error, 1)
	go func() { closed <- p.Close(ctx) }()
	select {
	case err := <-closed:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("close with active delivery = %v", err)
		}
	case <-time.After(time.Second):
		t.Error("attention delivery blocked the shutdown deadline")
	}
	close(hub.release)
	testutil.FailErr(t, "drain attention delivery", p.Close(t.Context()))
}
