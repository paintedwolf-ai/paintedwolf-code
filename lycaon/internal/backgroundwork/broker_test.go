package backgroundwork

import (
	"errors"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

func waitForPending(t *testing.T, broker *Broker, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		broker.mu.Lock()
		got := len(broker.pending)
		broker.mu.Unlock()
		if got == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("pending work did not reach %d", want)
}

func TestBrokerPrioritizesInteractiveWaiter(t *testing.T) {
	b := New(map[Resource]Limits{ResourceIO: {Total: 1}})
	release, err := b.Acquire(t.Context(), Request{Resources: []Resource{ResourceIO}})
	testutil.FailErr(t, "occupy io", err)
	order := make(chan string, 2)
	acquire := func(name string, priority Priority) {
		go func() {
			done, acquireErr := b.Acquire(t.Context(), Request{Priority: priority, Resources: []Resource{ResourceIO}})
			if acquireErr != nil {
				order <- acquireErr.Error()
				return
			}
			order <- name
			done()
		}()
	}
	acquire("proactive", PriorityProactive)
	waitForPending(t, b, 1)
	acquire("interactive", PriorityInteractive)
	waitForPending(t, b, 2)
	release()
	if got := <-order; got != "interactive" {
		t.Fatalf("first grant = %q want interactive", got)
	}
}

func TestBrokerSupersedesOlderQueuedEpoch(t *testing.T) {
	b := New(map[Resource]Limits{ResourceMetadata: {Total: 1}})
	release, err := b.Acquire(t.Context(), Request{Resources: []Resource{ResourceMetadata}})
	testutil.FailErr(t, "occupy metadata", err)
	older := make(chan error, 1)
	go func() {
		_, acquireErr := b.Acquire(t.Context(), Request{
			Key: "root:catalog", Epoch: 1, Resources: []Resource{ResourceMetadata},
		})
		older <- acquireErr
	}()
	waitForPending(t, b, 1)
	newer := make(chan error, 1)
	go func() {
		done, acquireErr := b.Acquire(t.Context(), Request{
			Key: "root:catalog", Epoch: 2, Resources: []Resource{ResourceMetadata},
		})
		if done != nil {
			done()
		}
		newer <- acquireErr
	}()
	if got := <-older; !errors.Is(got, ErrSuperseded) {
		t.Fatalf("older error = %v want superseded", got)
	}
	release()
	if got := <-newer; got != nil {
		t.Fatalf("newer error = %v", got)
	}
}

func TestBrokerPerLaneCapacityAdmitsDistinctLanes(t *testing.T) {
	b := New(map[Resource]Limits{ResourceMetadata: {Total: 4, PerLane: 1}})
	first, err := b.Acquire(t.Context(), Request{Lane: "/a", Resources: []Resource{ResourceMetadata}})
	testutil.FailErr(t, "occupy lane a", err)
	defer first()

	other, err := b.Acquire(t.Context(), Request{Lane: "/b", Resources: []Resource{ResourceMetadata}})
	testutil.FailErr(t, "acquire lane b", err)
	defer other()

	sameLane := make(chan error, 1)
	go func() {
		done, acquireErr := b.Acquire(t.Context(), Request{
			Lane: "/a", MaxWait: 50 * time.Millisecond, Resources: []Resource{ResourceMetadata},
		})
		if done != nil {
			done()
		}
		sameLane <- acquireErr
	}()
	if got := <-sameLane; !errors.Is(got, ErrAcquireTimeout) {
		t.Fatalf("second claim on an occupied lane = %v, want a timeout", got)
	}
}

func TestProcessDirectoryEnumerationCapacity(t *testing.T) {
	b := New(map[Resource]Limits{ResourceDirectory: {Total: 8, PerLane: 4}})
	releases := make([]func(), 0, 4)
	for range 4 {
		release, err := b.Acquire(t.Context(), Request{Lane: "/root-a", Resources: []Resource{ResourceDirectory}})
		testutil.FailErr(t, "acquire root a directory slot", err)
		releases = append(releases, release)
	}
	sameLane := make(chan error, 1)
	go func() {
		release, err := b.Acquire(t.Context(), Request{Lane: "/root-a", MaxWait: 40 * time.Millisecond, Resources: []Resource{ResourceDirectory}})
		if release != nil {
			release()
		}
		sameLane <- err
	}()
	if got := <-sameLane; !errors.Is(got, ErrAcquireTimeout) {
		t.Fatalf("fifth root-a directory slot = %v, want timeout", got)
	}
	for range 4 {
		release, err := b.Acquire(t.Context(), Request{Lane: "/root-b", Resources: []Resource{ResourceDirectory}})
		testutil.FailErr(t, "acquire root b directory slot", err)
		releases = append(releases, release)
	}
	global := make(chan error, 1)
	go func() {
		release, err := b.Acquire(t.Context(), Request{Lane: "/root-c", MaxWait: 40 * time.Millisecond, Resources: []Resource{ResourceDirectory}})
		if release != nil {
			release()
		}
		global <- err
	}()
	if got := <-global; !errors.Is(got, ErrAcquireTimeout) {
		t.Fatalf("ninth directory slot = %v, want timeout", got)
	}
	for _, release := range releases {
		release()
	}
}

func TestBrokerAgesProactiveAheadOfLaterInteractive(t *testing.T) {
	b := New(map[Resource]Limits{ResourceIO: {Total: 1}})
	release, err := b.Acquire(t.Context(), Request{Resources: []Resource{ResourceIO}})
	testutil.FailErr(t, "occupy io", err)

	order := make(chan string, 2)
	acquire := func(name string, priority Priority) {
		go func() {
			done, acquireErr := b.Acquire(t.Context(), Request{Priority: priority, Resources: []Resource{ResourceIO}})
			if acquireErr != nil {
				order <- acquireErr.Error()
				return
			}
			order <- name
			done()
		}()
	}
	acquire("proactive", PriorityProactive)
	waitForPending(t, b, 1)

	// The proactive waiter has now queued long enough to earn both bands.
	b.mu.Lock()
	b.now = func() time.Time { return time.Now().Add(2 * AgingInterval) }
	b.mu.Unlock()

	acquire("interactive", PriorityInteractive)
	waitForPending(t, b, 2)
	release()
	if got := <-order; got != "proactive" {
		t.Fatalf("first grant = %q, want the aged proactive waiter", got)
	}
}

func TestBrokerAcquireDeadlineReleasesTheWaiter(t *testing.T) {
	b := New(map[Resource]Limits{ResourceCPU: {Total: 1}})
	release, err := b.Acquire(t.Context(), Request{Resources: []Resource{ResourceCPU}})
	testutil.FailErr(t, "occupy cpu", err)
	defer release()

	_, err = b.Acquire(t.Context(), Request{
		MaxWait: 40 * time.Millisecond, Resources: []Resource{ResourceCPU},
	})
	if !errors.Is(err, ErrAcquireTimeout) {
		t.Fatalf("acquire error = %v, want a timeout", err)
	}
	b.mu.Lock()
	pending := len(b.pending)
	b.mu.Unlock()
	if pending != 0 {
		t.Fatalf("expired waiter still queued: %d pending", pending)
	}
}

func TestBrokerReservesWeightedCPUCapacity(t *testing.T) {
	b := New(map[Resource]Limits{ResourceCPU: {Total: 4}})
	releaseHeavy, err := b.Acquire(t.Context(), Request{
		Resources: []Resource{ResourceCPU}, Units: map[Resource]int{ResourceCPU: 3},
	})
	testutil.FailErr(t, "acquire heavy CPU work", err)
	defer releaseHeavy()

	releaseLight, err := b.Acquire(t.Context(), Request{
		Resources: []Resource{ResourceCPU}, Units: map[Resource]int{ResourceCPU: 1},
	})
	testutil.FailErr(t, "acquire remaining CPU unit", err)
	defer releaseLight()

	_, err = b.Acquire(t.Context(), Request{
		Resources: []Resource{ResourceCPU}, Units: map[Resource]int{ResourceCPU: 2},
		MaxWait: 40 * time.Millisecond,
	})
	if !errors.Is(err, ErrAcquireTimeout) {
		t.Fatalf("weighted acquire error = %v, want timeout", err)
	}
}

func TestBrokerOversizedCPURequestRunsAlone(t *testing.T) {
	b := New(map[Resource]Limits{ResourceCPU: {Total: 2}})
	release, err := b.Acquire(t.Context(), Request{
		Resources: []Resource{ResourceCPU}, Units: map[Resource]int{ResourceCPU: 8},
	})
	testutil.FailErr(t, "acquire oversized CPU work", err)
	b.mu.Lock()
	used := b.used[ResourceCPU]
	b.mu.Unlock()
	if used != 2 {
		t.Fatalf("reserved CPU units = %d want host capacity 2", used)
	}
	release()
}
