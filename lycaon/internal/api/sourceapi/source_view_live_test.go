package sourceapi

import (
	"context"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/api/taskgroup"
)

func TestTreeReviewWaitCoalescesBurstAndBoundsContinuousChanges(t *testing.T) {
	wake := make(chan struct{}, 1)
	done := make(chan bool, 1)
	go func() {
		done <- waitTreeReviewQuiet(t.Context(), wake, 150*time.Millisecond, 300*time.Millisecond)
	}()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	notices := 0
	for {
		select {
		case result := <-done:
			if !result || notices < 2 {
				t.Fatalf("review failed to coalesce structural burst: result=%v notices=%d", result, notices)
			}
			return
		case <-ticker.C:
			select {
			case wake <- struct{}{}:
				notices++
			default:
			}
		case <-t.Context().Done():
			t.Fatal("continuous changes starved review convergence")
		}
	}
}

func TestTreeReviewQuietWaitStopsWithView(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if waitTreeReviewQuiet(ctx, make(chan struct{}), time.Hour, time.Hour) {
		t.Fatal("closed view scheduled review work")
	}
}

func newTestTreeRefresher(ctx context.Context, refresh func(*treeRefresher)) (*treeRefresher, *taskgroup.Group) {
	group := &taskgroup.Group{}
	r := &treeRefresher{background: group, ctx: ctx, wake: make(chan struct{}, 1), quiet: time.Millisecond, maximum: time.Second}
	r.refresh = func() { refresh(r) }
	return r, group
}

func TestTreeRefreshIsDrainedWithTheBackground(t *testing.T) {
	refreshes := 0
	r, group := newTestTreeRefresher(t.Context(), func(*treeRefresher) { refreshes++ })
	r.notify()
	r.notify()
	group.Wait(t.Context())
	if refreshes != 1 {
		t.Fatalf("one burst refreshed %d times before the drain returned, want 1", refreshes)
	}
}

func TestTreeRefreshRunsAgainForAChangeDuringRefresh(t *testing.T) {
	refreshes := 0
	r, group := newTestTreeRefresher(t.Context(), func(r *treeRefresher) {
		refreshes++
		if refreshes == 1 {
			r.notify()
		}
	})
	r.notify()
	group.Wait(t.Context())
	if refreshes != 2 {
		t.Fatalf("a change during the refresh left %d refreshes, want 2", refreshes)
	}
	r.notify()
	group.Wait(t.Context())
	if refreshes != 3 {
		t.Fatalf("a later burst left %d refreshes, want 3", refreshes)
	}
}

func TestTreeRefreshStopsWithView(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	refreshes := 0
	r, group := newTestTreeRefresher(ctx, func(*treeRefresher) { refreshes++ })
	r.notify()
	group.Wait(t.Context())
	if refreshes != 0 {
		t.Fatalf("a closed view refreshed %d times", refreshes)
	}
}
