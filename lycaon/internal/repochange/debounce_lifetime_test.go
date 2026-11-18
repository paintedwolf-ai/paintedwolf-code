package repochange

import (
	"context"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestDebouncerRetirementJoinsDeliveryAndSealsAdmission(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		root := t.TempDir()
		entered, release := make(chan struct{}), make(chan struct{})
		var deliveries atomic.Int32
		t.Cleanup(RegisterObserver(func(_ context.Context, event Event) {
			if event.ProjectDir != root {
				return
			}
			deliveries.Add(1)
			close(entered)
			<-release
		}))
		d := NewDebouncer(time.Hour)
		d.NotifyWorktree(t.Context(), root, []string{"first"}, SourceMutation)
		settled := make(chan error, 1)
		go func() { settled <- d.retire(t.Context()) }()
		<-entered
		synctest.Wait()
		select {
		case err := <-settled:
			t.Fatalf("retirement returned before delivery completed: %v", err)
		default:
		}
		d.NotifyWorktree(t.Context(), root, []string{"late"}, SourceMutation)
		close(release)
		if err := <-settled; err != nil {
			t.Fatalf("retire debounce delivery: %v", err)
		}
		if deliveries.Load() != 1 {
			t.Fatalf("deliveries = %d, want 1", deliveries.Load())
		}
		if err := d.retire(t.Context()); err != nil {
			t.Fatalf("repeat retirement: %v", err)
		}
	})
}

func TestWaitQuietIncludesInFlightDelivery(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		root := t.TempDir()
		entered, release := make(chan struct{}), make(chan struct{})
		t.Cleanup(RegisterObserver(func(_ context.Context, event Event) {
			if event.ProjectDir == root {
				close(entered)
				<-release
			}
		}))
		d := NewDebouncer(time.Millisecond)
		d.NotifyWorktree(t.Context(), root, []string{"file"}, SourceMutation)
		<-entered
		quiet := make(chan struct{})
		go func() {
			d.WaitQuiet(t.Context(), root, time.Millisecond, time.Hour)
			close(quiet)
		}()
		time.Sleep(10 * time.Millisecond)
		select {
		case <-quiet:
			t.Fatal("reported quiet while an observer was delivering")
		default:
		}
		close(release)
		<-quiet
		if err := d.retire(t.Context()); err != nil {
			t.Fatalf("retire debounce delivery: %v", err)
		}
	})
}
