package hostpower

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/pkg/api"
)

type fakeInhibitor struct {
	mu       sync.Mutex
	acquires int
	leases   []*fakeLease
	nilLease bool
}

func (*fakeInhibitor) Supported() bool { return true }

func (f *fakeInhibitor) Acquire() (lease, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.acquires++
	if f.nilLease {
		return nil, nil
	}
	lease := &fakeLease{done: make(chan error, 1)}
	f.leases = append(f.leases, lease)
	return lease, nil
}

func (f *fakeInhibitor) acquireCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.acquires
}

type fakeLease struct {
	done     chan error
	released bool
}

func (l *fakeLease) Done() <-chan error { return l.done }
func (l *fakeLease) Release() error {
	if !l.released {
		l.released = true
		l.done <- errors.New("released")
		close(l.done)
	}
	return nil
}

func TestControllerHoldsOneLeaseAcrossOverlappingWork(t *testing.T) {
	inhibitor := &fakeInhibitor{}
	controller := newController(true, inhibitor)
	defer func() { _ = controller.Close() }()

	controller.ObserveTurnClock(api.TurnClock{SessionID: "root", Running: true})
	controller.ObserveActivity(api.ActivityEvent{ActivityID: "tool", Status: api.ActivityStatusActive})
	if got := controller.Snapshot(); !got.Inhibiting || got.ActiveWorkCount != 2 {
		t.Fatalf("active status = %+v", got)
	}
	if inhibitor.acquires != 1 {
		t.Fatalf("acquires = %d, want 1", inhibitor.acquires)
	}

	controller.ObserveTurnClock(api.TurnClock{SessionID: "root", Running: false})
	if got := controller.Snapshot(); !got.Inhibiting || got.ActiveWorkCount != 1 {
		t.Fatalf("overlap status = %+v", got)
	}
	controller.ObserveActivity(api.ActivityEvent{ActivityID: "tool", Status: api.ActivityStatusDone})
	if got := controller.Snapshot(); got.Inhibiting || got.ActiveWorkCount != 0 {
		t.Fatalf("idle status = %+v", got)
	}
}

func TestControllerSettingAppliesImmediately(t *testing.T) {
	inhibitor := &fakeInhibitor{}
	controller := newController(false, inhibitor)
	defer func() { _ = controller.Close() }()
	controller.SetActive("worker:one", true)
	if controller.Snapshot().Inhibiting {
		t.Fatal("disabled controller inhibited sleep")
	}
	controller.SetEnabled(true)
	if !controller.Snapshot().Inhibiting {
		t.Fatal("enabling with active work did not inhibit sleep")
	}
	controller.SetEnabled(false)
	if controller.Snapshot().Inhibiting {
		t.Fatal("disabling did not release assertion")
	}
}

func TestControllerConsumesDurableWorkerAndScanEdges(t *testing.T) {
	inhibitor := &fakeInhibitor{}
	controller := newController(true, inhibitor)
	defer func() { _ = controller.Close() }()

	worker := []byte(`{"worker_id":"worker-one","status":"pending"}`)
	if err := controller.ObserveDelivered(api.EventTopicWorker, worker); err != nil {
		t.Fatalf("observe worker: %v", err)
	}
	scan := []byte(`{"scan_id":"scan-one","status":"running"}`)
	if err := controller.ObserveDelivered(api.EventTopicScan, scan); err != nil {
		t.Fatalf("observe scan: %v", err)
	}
	if got := controller.Snapshot().ActiveWorkCount; got != 2 {
		t.Fatalf("active count = %d, want 2", got)
	}

	worker = []byte(`{"worker_id":"worker-one","status":"complete"}`)
	if err := controller.ObserveDelivered(api.EventTopicWorker, worker); err != nil {
		t.Fatalf("complete worker: %v", err)
	}
	scan = []byte(`{"scan_id":"scan-one","status":"complete"}`)
	if err := controller.ObserveDelivered(api.EventTopicScan, scan); err != nil {
		t.Fatalf("complete scan: %v", err)
	}
	if got := controller.Snapshot(); got.ActiveWorkCount != 0 || got.Inhibiting {
		t.Fatalf("terminal status = %+v", got)
	}
}

func TestControllerIgnoresEdgesWithoutIdentity(t *testing.T) {
	controller := newController(true, &fakeInhibitor{})
	defer func() { _ = controller.Close() }()

	controller.ObserveActivity(api.ActivityEvent{Status: api.ActivityStatusActive})
	controller.ObserveTurnClock(api.TurnClock{Running: true})
	if err := controller.ObserveDelivered(api.EventTopicWorker, []byte(`{"status":"pending"}`)); err != nil {
		t.Fatalf("observe worker: %v", err)
	}
	if err := controller.ObserveDelivered(api.EventTopicScan, []byte(`{"status":"running"}`)); err != nil {
		t.Fatalf("observe scan: %v", err)
	}
	if got := controller.Snapshot(); got.ActiveWorkCount != 0 || got.Inhibiting {
		t.Fatalf("status = %+v", got)
	}
}

func TestControllerRejectsNilLease(t *testing.T) {
	controller := newController(true, &fakeInhibitor{nilLease: true})
	defer func() { _ = controller.Close() }()
	controller.SetActive("worker:one", true)
	got := controller.Snapshot()
	if got.Inhibiting || got.LastError == "" {
		t.Fatalf("status = %+v", got)
	}
	controller.SetActive("worker:one", false)
	if got := controller.Snapshot(); got.LastError != "" {
		t.Fatalf("idle status retained error: %+v", got)
	}
}

func TestControllerReacquiresAfterUnexpectedExit(t *testing.T) {
	inhibitor := &fakeInhibitor{}
	controller := newController(true, inhibitor)
	controller.retryDelay = time.Millisecond
	defer func() { _ = controller.Close() }()

	controller.SetActive("worker:one", true)
	inhibitor.mu.Lock()
	first := inhibitor.leases[0]
	inhibitor.mu.Unlock()
	first.done <- errors.New("assertion exited")
	close(first.done)

	deadline := time.After(time.Second)
	for {
		status := controller.Snapshot()
		if inhibitor.acquireCount() >= 2 && status.Inhibiting && status.LastError == "" {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("assertion was not reacquired: %+v", status)
		default:
			time.Sleep(time.Millisecond)
		}
	}
}

func TestControllerCloseIsTerminal(t *testing.T) {
	inhibitor := &fakeInhibitor{}
	controller := newController(true, inhibitor)
	controller.SetActive("worker:one", true)
	if err := controller.Close(); err != nil {
		t.Fatalf("close controller: %v", err)
	}
	controller.SetActive("worker:two", true)
	controller.SetEnabled(true)
	if got := inhibitor.acquireCount(); got != 1 {
		t.Fatalf("acquires = %d, want 1", got)
	}
}
