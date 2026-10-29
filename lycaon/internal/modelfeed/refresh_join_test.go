package modelfeed

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

type refreshOutcome struct {
	doc *Document
	err error
}

// gatedFeed holds every fetch until release closes, reporting each start.
type gatedFeed struct {
	feed    *Feed
	started chan struct{}
	release chan struct{}
	fetches atomic.Int32
}

func newGatedFeed(t *testing.T) *gatedFeed {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "models_dev_fixture.json"))
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	g := &gatedFeed{started: make(chan struct{}, 8), release: make(chan struct{})}
	g.feed, err = New(Options{CacheDir: t.TempDir(), TTL: time.Hour, GetBytes: func(ctx context.Context, _ string) ([]byte, error) {
		g.fetches.Add(1)
		g.started <- struct{}{}
		select {
		case <-g.release:
			return raw, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return g
}

func refreshInBackground(f *Feed, ctx context.Context) <-chan refreshOutcome {
	out := make(chan refreshOutcome, 1)
	go func() {
		doc, err := f.Refresh(ctx)
		out <- refreshOutcome{doc: doc, err: err}
	}()
	return out
}

func TestRefreshSharesTheRunningFetch(t *testing.T) {
	g := newGatedFeed(t)
	owner := refreshInBackground(g.feed, t.Context())
	<-g.started

	flight, isOwner := g.feed.joinRefresh()
	if isOwner {
		t.Fatal("a second caller started its own fetch beside the running one")
	}
	// A caller whose own context ends leaves without starting a second fetch.
	ended, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := g.feed.Document(ended); !errors.Is(err, context.Canceled) {
		t.Fatalf("Document with an ended context = %v, want context.Canceled", err)
	}
	if n := g.fetches.Load(); n != 1 {
		t.Fatalf("fetches after an ended caller = %d, want 1", n)
	}

	close(g.release)
	got := <-owner
	<-flight.done
	if got.err != nil || flight.err != nil {
		t.Fatalf("owner err = %v, shared err = %v", got.err, flight.err)
	}
	if got.doc == nil || flight.doc != got.doc {
		t.Fatalf("shared document %p, want the owner's %p", flight.doc, got.doc)
	}
	if n := g.fetches.Load(); n != 1 {
		t.Fatalf("fetches after a shared refresh = %d, want 1", n)
	}
	if _, isOwner := g.feed.joinRefresh(); !isOwner {
		t.Fatal("the finished fetch still holds later callers")
	}
}

func TestRefreshJoinerOutlivesTheOwnersCancellation(t *testing.T) {
	g := newGatedFeed(t)
	ownerCtx, cancelOwner := context.WithCancel(t.Context())
	owner := refreshInBackground(g.feed, ownerCtx)
	<-g.started
	joined := refreshInBackground(g.feed, t.Context())

	cancelOwner()
	if got := <-owner; !errors.Is(got.err, context.Canceled) {
		t.Fatalf("cancelled owner err = %v, want context.Canceled", got.err)
	}
	// The joiner fetches for itself once the owner has given up.
	<-g.started
	close(g.release)
	shared := <-joined
	if shared.err != nil || shared.doc == nil {
		t.Fatalf("joiner after the owner's cancellation: doc=%v err=%v", shared.doc, shared.err)
	}
	if n := g.fetches.Load(); n != 2 {
		t.Fatalf("fetches = %d, want the owner's and the joiner's", n)
	}
	if !g.feed.Usable() {
		t.Fatal("expected the joiner's fetch to publish the document")
	}
}
