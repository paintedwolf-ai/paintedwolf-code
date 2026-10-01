package backgroundwork

import (
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestLongBackgroundWorkLeavesInteractiveCapacity(t *testing.T) {
	broker := New(map[Resource]Limits{ResourceCPU: {Total: 2, InteractiveReserve: 1}})
	background := Request{
		Priority: PriorityProactive, Resources: []Resource{ResourceCPU},
		Units: map[Resource]int{ResourceCPU: 2},
	}
	release, err := broker.Acquire(t.Context(), background)
	testutil.FailErr(t, "start long background work", err)
	defer release()
	queued := make(chan error, 1)
	go func() {
		done, err := broker.Acquire(t.Context(), background)
		if err == nil {
			done()
		}
		queued <- err
	}()
	waitForPending(t, broker, 1)
	interactive, err := broker.Acquire(t.Context(), Request{
		Priority: PriorityInteractive, Resources: []Resource{ResourceCPU}, MaxWait: time.Second,
	})
	testutil.FailErr(t, "admit interactive work without finishing the background job", err)
	interactive()
	select {
	case err := <-queued:
		t.Fatalf("background job consumed the interactive reserve: %v", err)
	default:
	}
	release()
	testutil.FailErr(t, "resume queued background work", <-queued)
}

func TestPromotionAndReleaseKeepReservationAccountingStable(t *testing.T) {
	broker := New(map[Resource]Limits{ResourceCPU: {Total: 3, InteractiveReserve: 1}})
	interests := &PriorityGroup{}
	remove := interests.Add(PriorityInteractive)
	release, err := broker.Acquire(t.Context(), Request{
		Priority: PriorityProactive, Interests: interests, Resources: []Resource{ResourceCPU},
		Units: map[Resource]int{ResourceCPU: 3},
	})
	testutil.FailErr(t, "admit shared interactive work", err)
	remove()
	release()
	broker.mu.Lock()
	defer broker.mu.Unlock()
	if broker.used[ResourceCPU] != 0 || broker.backgroundUsed[ResourceCPU] != 0 {
		t.Fatalf("release changed its admitted weight: total=%d background=%d", broker.used[ResourceCPU], broker.backgroundUsed[ResourceCPU])
	}
}
