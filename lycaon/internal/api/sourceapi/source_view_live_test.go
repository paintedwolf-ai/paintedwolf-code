package sourceapi

import (
	"context"
	"testing"
	"time"
)

func TestTreeReviewWaitCoalescesBurstAndBoundsContinuousChanges(t *testing.T) {
	wake := make(chan struct{}, 1)
	done := make(chan bool, 1)
	go func() {
		done <- waitTreeReviewQuiet(t.Context(), wake, 150*time.Millisecond, 300*time.Millisecond)
	}()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
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
		case <-deadline.C:
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
