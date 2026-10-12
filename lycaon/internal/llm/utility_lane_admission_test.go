package llm

import (
	"context"
	"errors"
	"runtime"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

func awaitForegroundWaiter(t *testing.T, lanes *utilityLanes, id string) {
	t.Helper()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for {
		lanes.mu.Lock()
		lane := lanes.lanes[id]
		waiting := lane != nil && lane.foregroundWaiting > 0
		lanes.mu.Unlock()
		if waiting {
			return
		}
		select {
		case <-deadline.C:
			t.Fatal("foreground did not enter admission")
		default:
			runtime.Gosched()
		}
	}
}

func TestAdmissionPrioritizesWaitingForeground(t *testing.T) {
	lanes := newUtilityLanes()
	release, err := lanes.acquire(t.Context(), "local", true, UtilityClassQuality)
	testutil.FailErr(t, "hold utility", err)
	t.Cleanup(release)
	foreground := make(chan func(), 1)
	go func() {
		done, err := lanes.acquireForeground(t.Context(), "local", true)
		if err != nil {
			t.Error(err)
			return
		}
		foreground <- done
	}()
	awaitForegroundWaiter(t, lanes, "local")
	utility := make(chan func(), 1)
	go func() {
		done, err := lanes.acquire(t.Context(), "local", true, UtilityClassRequested)
		if err != nil {
			t.Error(err)
			return
		}
		utility <- done
	}()
	release()
	var finish func()
	select {
	case finish = <-foreground:
	case done := <-utility:
		done()
		t.Fatal("utility overtook queued foreground")
	case <-time.After(time.Second):
		t.Fatal("foreground was not admitted")
	}
	t.Cleanup(finish)
	select {
	case done := <-utility:
		done()
		t.Fatal("utility overlapped foreground")
	default:
	}
	finish()
	select {
	case done := <-utility:
		done()
	case <-time.After(time.Second):
		t.Fatal("utility did not resume")
	}
}

func TestAdmissionCancellationReleasesForegroundPriority(t *testing.T) {
	lanes := newUtilityLanes()
	release, err := lanes.acquire(t.Context(), "local", true, UtilityClassQuality)
	testutil.FailErr(t, "hold utility", err)
	t.Cleanup(release)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	result := make(chan error, 1)
	go func() { _, err := lanes.acquireForeground(ctx, "local", true); result <- err }()
	awaitForegroundWaiter(t, lanes, "local")
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel = %v", err)
	}
	release()
	done, err := lanes.acquire(t.Context(), "local", true, UtilityClassQuality)
	testutil.FailErr(t, "utility after canceled foreground", err)
	done()
}

func TestAdmissionRejectsCanceledContextBeforeTakingLane(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	lanes := newUtilityLanes()
	_, err := lanes.acquire(ctx, "local", true, UtilityClassOverlay)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled overlay = %v", err)
	}
	done, err := lanes.acquireForeground(t.Context(), "local", true)
	testutil.FailErr(t, "foreground after cancellation", err)
	done()
	done()
	done, err = lanes.acquire(t.Context(), "local", true, UtilityClassRequested)
	testutil.FailErr(t, "utility after repeated release", err)
	done()
}

func TestAdmissionIsolationAndOptionalPlane(t *testing.T) {
	var lanes *utilityLanes
	done, err := lanes.acquireForeground(t.Context(), "local", true)
	testutil.FailErr(t, "absent lane", err)
	done()
	var plane *UtilityPlane
	done, err = plane.acquireForeground(t.Context(), nil)
	testutil.FailErr(t, "absent plane", err)
	done()
	lanes = newUtilityLanes()
	done, err = lanes.acquireForeground(t.Context(), "local-a", true)
	testutil.FailErr(t, "first provider", err)
	defer done()
	other, err := lanes.acquire(t.Context(), "local-b", true, UtilityClassQuality)
	testutil.FailErr(t, "independent provider", err)
	other()
}
