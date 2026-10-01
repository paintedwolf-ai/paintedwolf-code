package session

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestCompactionRunnerSingleFlight(t *testing.T) {
	r := NewCompactionRunner()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var runs atomic.Int32
	start := make(chan struct{})
	r.Trigger(ctx, "s1", func(context.Context) {
		<-start
		runs.Add(1)
	})
	r.Trigger(ctx, "s1", func(context.Context) {
		runs.Add(1)
	})
	close(start)
	r.Wait()
	if got := runs.Load(); got != 2 {
		t.Fatalf("runs = %d want 2 (initial + coalesced re-run)", got)
	}
}

func TestCompactionRunnerSurvivesCallerCancel(t *testing.T) {
	r := NewCompactionRunner()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	r.Trigger(ctx, "s1", func(bg context.Context) {
		defer close(done)
		time.Sleep(20 * time.Millisecond)
		if bg.Err() != nil {
			t.Errorf("background ctx canceled: %v", bg.Err())
		}
	})
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("background pass did not finish")
	}
	r.Wait()
}

func TestCompactionRunnerCancelSessionJoinsWorkAndDropsPending(t *testing.T) {
	r := NewCompactionRunner()
	started := make(chan struct{})
	finished := make(chan struct{})
	var runs atomic.Int32
	r.Trigger(context.Background(), "s1", func(ctx context.Context) {
		runs.Add(1)
		close(started)
		<-ctx.Done()
		close(finished)
	})
	<-started
	r.Trigger(context.Background(), "s1", func(context.Context) {
		runs.Add(1)
	})
	r.CancelSession("s1")
	select {
	case <-finished:
	default:
		t.Fatal("CancelSession returned before the pass finished")
	}
	if got := runs.Load(); got != 1 {
		t.Fatalf("runs = %d want 1; pending pass must be dropped", got)
	}
}

func TestCompactionExecutionSerializesManualAndBackground(t *testing.T) {
	r := NewCompactionRunner()
	entered := make(chan struct{})
	release := make(chan struct{})
	manualDone := make(chan error, 1)
	go func() {
		manualDone <- r.Execute(t.Context(), "session", func(context.Context) error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered
	backgroundDone := make(chan struct{})
	r.Trigger(t.Context(), "session", func(ctx context.Context) {
		err := r.Execute(ctx, "session", func(context.Context) error {
			select {
			case <-release:
			default:
				t.Error("background overlapped manual compaction")
			}
			close(backgroundDone)
			return nil
		})
		if err != nil {
			t.Errorf("background execution: %v", err)
		}
	})
	close(release)
	if err := <-manualDone; err != nil {
		t.Fatalf("manual execution: %v", err)
	}
	r.Wait()
	select {
	case <-backgroundDone:
	default:
		t.Fatal("background execution lost")
	}
}

func TestCompactionExecutionCancellationJoinsManual(t *testing.T) {
	r := NewCompactionRunner()
	entered := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- r.Execute(t.Context(), "session", func(ctx context.Context) error {
			close(entered)
			<-ctx.Done()
			return ctx.Err()
		})
	}()
	<-entered
	r.CancelSession("session")
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("manual cancellation: %v", err)
	}
	if err := r.Execute(t.Context(), "session", func(context.Context) error { return nil }); err != nil {
		t.Fatalf("execute after cancellation: %v", err)
	}
	r.Wait()
}
