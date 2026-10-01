package pagedview

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRegistryPinsBudgetsAndScope(t *testing.T) {
	budget := NewBudget(100)
	registry := NewRegistry[int](budget, 2, time.Minute)
	now := time.Now()
	registry.clock = func() time.Time { return now }
	scope := Scope{Person: "person", Project: "project", Workspace: "workspace"}
	disposed := 0
	id, err := registry.Put(scope, 42, 80, func(int) { disposed++ })
	testutil.FailErr(t, "retain presentation", err)
	_, _, err = registry.Acquire(Scope{Person: "foreign"}, id)
	if !errors.Is(err, ErrExpired) {
		t.Fatalf("foreign scope: %v", err)
	}
	registry.Release(Scope{Person: "foreign"}, id)
	value, release, err := registry.Acquire(scope, id)
	testutil.FailErr(t, "pin presentation", err)
	if value != 42 {
		t.Fatalf("value: %d", value)
	}
	_, err = registry.Put(scope, 43, 30, nil)
	if !errors.Is(err, ErrBudget) {
		t.Fatalf("pinned budget: %v", err)
	}
	registry.Release(scope, id)
	if disposed != 0 || budget.Used() != 80 {
		t.Fatal("active read lost its resource")
	}
	_, _, err = registry.Acquire(scope, id)
	if !errors.Is(err, ErrExpired) {
		t.Fatalf("expired handle: %v", err)
	}
	release()
	release()
	if disposed != 1 || budget.Used() != 0 {
		t.Fatal("resource was not disposed exactly once")
	}
	registry.Release(scope, id)
	now = now.Add(2 * time.Minute)
	_, _, err = registry.Acquire(scope, id)
	if !errors.Is(err, ErrExpired) {
		t.Fatalf("long-released handle: %v", err)
	}
	_, _, err = registry.Acquire(scope, "never-issued")
	if !errors.Is(err, ErrExpired) {
		t.Fatalf("unknown handle: %v", err)
	}
}

func TestRegistryDisposalMayReenter(t *testing.T) {
	registry := NewRegistry[int](NewBudget(100), 2, time.Minute)
	scope := Scope{Person: "person"}
	id, err := registry.Put(scope, 1, 50, func(int) { registry.Close() })
	testutil.FailErr(t, "retain", err)
	registry.Release(scope, id)
	_, err = registry.Put(scope, 2, 50, nil)
	if !errors.Is(err, ErrExpired) {
		t.Fatalf("closed registry: %v", err)
	}
}

func TestPreparationCancelsOnlyLastInterest(t *testing.T) {
	var work Preparation[string, int]
	started := make(chan struct{})
	finish := make(chan struct{})
	canceled := make(chan struct{})
	var calls atomic.Int32
	prepare := func(ctx context.Context) (int, error) {
		calls.Add(1)
		close(started)
		select {
		case <-finish:
			return 7, nil
		case <-ctx.Done():
			close(canceled)
			return 0, ctx.Err()
		}
	}
	first, cancel := context.WithCancel(context.Background())
	firstDone := make(chan error, 1)
	go func() { _, err := work.Do(first, "source", prepare); firstDone <- err }()
	<-started
	// Install the second interest while holding the same lock used by Do.
	work.mu.Lock()
	flight := work.pending["source"]
	flight.interests++
	work.mu.Unlock()
	cancel()
	if err := <-firstDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("first waiter: %v", err)
	}
	select {
	case <-canceled:
		t.Fatal("other interest canceled")
	default:
	}
	close(finish)
	<-flight.done
	work.leave("source", flight, 0)
	if flight.result != 7 || calls.Load() != 1 {
		t.Fatal("shared result changed")
	}
}

func TestPreparationAbandonedWorkCannotCaptureNewWaiter(t *testing.T) {
	var work Preparation[string, int]
	ctx, cancel := context.WithCancel(context.Background())
	started, stopped := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := work.Do(ctx, "source", func(ctx context.Context) (int, error) {
			close(started)
			<-ctx.Done()
			close(stopped)
			return 1, ctx.Err()
		})
		done <- err
	}()
	<-started
	cancel()
	<-done
	<-stopped
	result, err := work.Do(context.Background(), "source", func(context.Context) (int, error) { return 2, nil })
	testutil.FailErr(t, "replacement preparation", err)
	if result != 2 {
		t.Fatal("replacement inherited abandoned work")
	}
}

func TestCommandsRetryBeforeRevisionCheck(t *testing.T) {
	commands := NewCommands[int](nil)
	t.Cleanup(commands.Close)
	initial := commands.Revision()
	calls := 0
	commit := func(string) (int, error) { calls++; return calls, nil }
	result, revision, err := commands.Apply(t.Context(), "request", initial, []byte("expand"), commit)
	testutil.FailErr(t, "initial intent", err)
	retried, same, err := commands.Apply(t.Context(), "request", initial, []byte("expand"), commit)
	testutil.FailErr(t, "uncertain retry", err)
	if result != retried || revision != same || calls != 1 {
		t.Fatal("retry repeated mutation")
	}
	_, _, err = commands.Apply(t.Context(), "request", revision, []byte("collapse"), commit)
	if !errors.Is(err, ErrOperationConflict) {
		t.Fatalf("conflicting retry: %v", err)
	}
	_, _, err = commands.Apply(t.Context(), "another", initial, []byte("collapse"), commit)
	if !errors.Is(err, ErrRevision) {
		t.Fatalf("stale intent: %v", err)
	}
}

func TestPreparationPublishesEnoughWithoutCancelingAnotherInterest(t *testing.T) {
	var work Preparation[string, int]
	start, publish, finish := make(chan struct{}), make(chan struct{}), make(chan struct{})
	prepared := func(ctx context.Context, update func(int), _ func() int) (int, error) {
		close(start)
		<-publish
		update(10)
		select {
		case <-finish:
			return 20, nil
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
	bounded := make(chan int, 1)
	go func() {
		value, _ := work.Join(t.Context(), "root", 10, func(value int) int { return value }, prepared)
		bounded <- value
	}()
	<-start
	work.mu.Lock()
	held := work.pending["root"]
	held.interests++
	work.mu.Unlock()
	close(publish)
	if got := <-bounded; got != 10 {
		t.Fatalf("bounded result=%d", got)
	}
	if held.ctx.Err() != nil {
		t.Fatal("bounded reader canceled remaining work")
	}
	close(finish)
	<-held.done
	work.leave("root", held, 0)
	if held.result != 20 || held.err != nil {
		t.Fatalf("final result=%d error=%v", held.result, held.err)
	}
}

func TestRegistryKeepsDisposalChargedUntilReleased(t *testing.T) {
	budget := NewBudget(100)
	registry := NewRegistry[int](budget, 1, time.Minute)
	scope := Scope{Person: "p"}
	charged := int64(0)
	_, err := registry.Put(scope, 1, 80, func(int) { charged = budget.Used() })
	testutil.FailErr(t, "retain initial value", err)
	_, err = registry.Put(scope, 2, 90, nil)
	testutil.FailErr(t, "replace retained value", err)
	if charged != 80 || budget.Used() != 90 {
		t.Fatalf("charge during disposal=%d after=%d", charged, budget.Used())
	}
	registry.Close()
}

func TestPreparationCloseDrainsAbandonedFlights(t *testing.T) {
	var work Preparation[string, int]
	started, canceled, finish := make(chan struct{}), make(chan struct{}), make(chan struct{})
	request, cancel := context.WithCancel(t.Context())
	returned := make(chan error, 1)
	go func() {
		_, err := work.Do(request, "source", func(ctx context.Context) (int, error) {
			close(started)
			<-ctx.Done()
			close(canceled)
			<-finish
			return 0, ctx.Err()
		})
		returned <- err
	}()
	<-started
	cancel()
	<-returned
	<-canceled
	drained := make(chan struct{})
	go func() { work.Close(); close(drained) }()
	select {
	case <-drained:
		t.Fatal("close returned while an abandoned preparation still retained resources")
	default:
	}
	close(finish)
	<-drained
	_, err := work.Do(t.Context(), "new", func(context.Context) (int, error) {
		t.Error("closed preparation accepted new work")
		return 0, nil
	})
	if !errors.Is(err, ErrExpired) {
		t.Fatalf("closed admission=%v", err)
	}
}

func TestSharedBudgetEvictsAcrossAdaptersAndPreservesPins(t *testing.T) {
	budget := NewBudget(100)
	left := NewRegistry[int](budget, 10, time.Hour)
	right := NewRegistry[int](budget, 10, time.Hour)
	defer left.Close()
	defer right.Close()
	scope := Scope{Person: "person", Project: "project"}
	first, err := left.Put(scope, 1, 60, nil)
	testutil.FailErr(t, "retain first adapter", err)
	_, release, err := left.Acquire(scope, first)
	testutil.FailErr(t, "pin first adapter", err)
	if _, err = right.Put(scope, 2, 60, nil); !errors.Is(err, ErrBudget) {
		t.Fatalf("active read was evicted: %v", err)
	}
	release()
	second, err := right.Put(scope, 2, 60, nil)
	testutil.FailErr(t, "reuse shared idle budget", err)
	if _, _, err = left.Acquire(scope, first); !errors.Is(err, ErrExpired) {
		t.Fatalf("idle resource retained: %v", err)
	}
	value, done, err := right.Acquire(scope, second)
	testutil.FailErr(t, "read replacement adapter", err)
	defer done()
	if value != 2 || budget.Used() != 60 {
		t.Fatalf("value=%d bytes=%d", value, budget.Used())
	}
}

func TestPutPinnedProtectsPreparationAndSweepExpiresIdleResources(t *testing.T) {
	budget := NewBudget(100)
	registry := NewRegistry[int](budget, 2, time.Minute)
	defer registry.Close()
	now := time.Now()
	registry.clock = func() time.Time { return now }
	scope := Scope{Person: "person", Project: "project"}
	id, release, err := registry.PutPinned(scope, 42, 100, nil)
	testutil.FailErr(t, "retain pinned preparation", err)
	now = now.Add(2 * time.Minute)
	registry.Sweep()
	_, err = registry.Put(scope, 43, 1, nil)
	if !errors.Is(err, ErrBudget) {
		t.Fatalf("preparation was evicted: %v", err)
	}
	release()
	now = now.Add(2 * time.Minute)
	registry.Sweep()
	if budget.Used() != 0 {
		t.Fatalf("idle retained bytes=%d", budget.Used())
	}
	_, _, err = registry.Acquire(scope, id)
	if !errors.Is(err, ErrExpired) {
		t.Fatalf("expired preparation=%v", err)
	}
}

func TestRegistryInvalidationInterruptsBeforeDisposalAndPreservesUnmatched(t *testing.T) {
	registry := NewRegistry[int](NewBudget(100), 4, time.Minute)
	defer registry.Close()
	removed := Scope{Project: "removed"}
	other := Scope{Project: "other"}
	interrupted := map[int]bool{}
	disposed := map[int]bool{}
	dispose := func(value int) {
		if !interrupted[value] {
			t.Errorf("disposed %d before interruption", value)
		}
		disposed[value] = true
	}
	idle, err := registry.Put(removed, 1, 10, dispose)
	testutil.FailErr(t, "retain idle view", err)
	active, release, err := registry.PutPinned(removed, 2, 10, dispose)
	testutil.FailErr(t, "retain active view", err)
	survivor, err := registry.Put(other, 3, 10, nil)
	testutil.FailErr(t, "retain other project", err)
	registry.Invalidate(func(value int) bool { return value != 3 }, func(value int) {
		interrupted[value] = true
		_, _, err := registry.Acquire(removed, idle)
		if !errors.Is(err, ErrExpired) {
			t.Errorf("removed entry still visible: %v", err)
		}
	})
	if !disposed[1] || disposed[2] || !interrupted[2] {
		t.Fatal("invalidation did not respect active reads")
	}
	_, _, err = registry.Acquire(removed, active)
	if !errors.Is(err, ErrExpired) {
		t.Fatalf("active entry remains available: %v", err)
	}
	release()
	if !disposed[2] {
		t.Fatal("finished read did not dispose invalidated view")
	}
	_, unpin, err := registry.Acquire(other, survivor)
	testutil.FailErr(t, "read other project", err)
	unpin()
}

func TestRegistryResizesPinnedResourceAndEvictsIdleAcrossAdapters(t *testing.T) {
	budget := NewBudget(100)
	left, right := NewRegistry[int](budget, 8, time.Hour), NewRegistry[int](budget, 8, time.Hour)
	defer left.Close()
	defer right.Close()
	scope := Scope{Person: "person", Project: "project"}
	id, release, err := left.PutPinned(scope, 1, 30, nil)
	testutil.FailErr(t, "reserve active resource", err)
	defer release()
	idle, err := right.Put(scope, 2, 60, nil)
	testutil.FailErr(t, "retain idle adapter resource", err)
	testutil.FailErr(t, "grow active resource by evicting idle", left.Resize(scope, id, 70))
	if _, _, err := right.Acquire(scope, idle); !errors.Is(err, ErrExpired) {
		t.Fatalf("idle resource was not evicted: %v", err)
	}
	testutil.FailErr(t, "converge retained bytes", left.Resize(scope, id, 20))
	if budget.Used() != 20 {
		t.Fatalf("converged usage = %d", budget.Used())
	}
	if err := left.Resize(Scope{Person: "another"}, id, 1); !errors.Is(err, ErrExpired) {
		t.Fatalf("resize crossed scope: %v", err)
	}
	if err := left.Resize(scope, id, 101); !errors.Is(err, ErrBudget) || budget.Used() != 20 {
		t.Fatalf("rejected growth changed usage: err=%v used=%d", err, budget.Used())
	}
}
