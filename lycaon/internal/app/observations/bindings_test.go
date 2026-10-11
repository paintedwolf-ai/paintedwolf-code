package observations

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workscope"
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
	go func() {
		repochange.Notify(t.Context(), repochange.Event{ProjectDir: t.TempDir(), Kind: repochange.WorktreeChanged})
		close(notifyDone)
	}()
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
	repochange.Notify(t.Context(), repochange.Event{ProjectDir: t.TempDir(), Kind: repochange.WorktreeChanged})
	if calls.Load() != 1 {
		t.Fatalf("closed observer calls = %d", calls.Load())
	}
}

func TestObserverReleaseRefusesAlreadyCopiedCallback(t *testing.T) {
	var work workscope.Group
	var calls atomic.Int32
	copied := ownedObserver(&work, func(context.Context, repochange.Event) { calls.Add(1) })
	copied(t.Context(), repochange.Event{})
	work.Stop()
	testutil.FailErr(t, "drain observer owner", work.Wait(t.Context()))
	copied(t.Context(), repochange.Event{})
	if calls.Load() != 1 {
		t.Fatalf("copied callback admitted after release: %d", calls.Load())
	}
}
