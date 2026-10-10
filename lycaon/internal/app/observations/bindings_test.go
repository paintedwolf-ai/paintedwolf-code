package observations

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/testutil"
)

type repositoryObserver func(context.Context, string)

func (observe repositoryObserver) Changed(ctx context.Context, path string) { observe(ctx, path) }

func TestObserverReleaseDrainsActiveCallback(t *testing.T) {
	entered, cancelled, finish := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	repository := repositoryObserver(func(ctx context.Context, _ string) {
		calls.Add(1)
		close(entered)
		<-ctx.Done()
		close(cancelled)
		<-finish
	})
	release := Bind(&events.Publisher{}, nil, nil, nil, nil, repository, nil, nil)
	notifyDone := make(chan struct{})
	go func() { repochange.Notify(t.Context(), repochange.Event{}); close(notifyDone) }()
	<-entered
	released := make(chan error, 1)
	go func() { released <- release(t.Context()) }()
	<-cancelled
	select {
	case err := <-released:
		t.Fatalf("release returned before callback finished: %v", err)
	default:
	}
	close(finish)
	<-notifyDone
	testutil.FailErr(t, "release observers", <-released)
	repochange.Notify(t.Context(), repochange.Event{})
	if calls.Load() != 1 {
		t.Fatalf("closed observer calls = %d", calls.Load())
	}
}

func TestObserverReleaseRefusesAlreadyCopiedCallback(t *testing.T) {
	entered, finish := make(chan struct{}), make(chan struct{})
	releaseBlocker := repochange.RegisterObserver(func(context.Context, repochange.Event) { close(entered); <-finish })
	t.Cleanup(releaseBlocker)
	var calls atomic.Int32
	release := Bind(&events.Publisher{}, nil, nil, nil, nil, repositoryObserver(func(context.Context, string) { calls.Add(1) }), nil, nil)
	notifyDone := make(chan struct{})
	go func() { repochange.Notify(t.Context(), repochange.Event{}); close(notifyDone) }()
	<-entered
	testutil.FailErr(t, "release copied observer", release(t.Context()))
	close(finish)
	<-notifyDone
	if calls.Load() != 0 {
		t.Fatalf("copied callback admitted after release: %d", calls.Load())
	}
}
