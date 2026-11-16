package projectliveness

import (
	"context"
	"sync"
	"testing"
	"time"
)

type testActivityChecker struct {
	hasActive map[string]bool
}

func (c *testActivityChecker) HasActiveProjectSessions(_ context.Context, projectID string, _ time.Time) (bool, error) {
	return c.hasActive[projectID], nil
}

type testHandler struct {
	mu        sync.Mutex
	activated []string
	parked    []string
}

func (h *testHandler) OnProjectActivate(_ context.Context, projectID string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.activated = append(h.activated, projectID)
	return nil
}

func (h *testHandler) OnProjectPark(_ context.Context, projectID string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.parked = append(h.parked, projectID)
	return nil
}

func TestClaimWorkspaceParksAfterGraceWhenNoActiveSessions(t *testing.T) {
	handler := &testHandler{}
	checker := &testActivityChecker{hasActive: map[string]bool{"proj-1": false}}
	tracker := New(Config{
		ParkGrace: 20 * time.Millisecond,
		Sessions:  checker,
		Handler:   handler,
	})
	defer tracker.Close()

	release := tracker.ClaimWorkspace("proj-1")
	if tracker.IsParked("proj-1") {
		t.Fatal("expected proj-1 to be active while claimed")
	}

	release()

	time.Sleep(5 * time.Millisecond)
	if tracker.IsParked("proj-1") {
		t.Fatal("expected proj-1 to still be warm inside grace window")
	}

	time.Sleep(25 * time.Millisecond)
	if !tracker.IsParked("proj-1") {
		t.Fatal("expected proj-1 to be parked after grace window")
	}

	handler.mu.Lock()
	parkedCount := len(handler.parked)
	handler.mu.Unlock()
	if parkedCount != 1 {
		t.Fatalf("expected 1 park call, got %d", parkedCount)
	}
}

func TestSwitchBetweenProjectsWithActiveSessionsDoesNotPark(t *testing.T) {
	handler := &testHandler{}
	checker := &testActivityChecker{hasActive: map[string]bool{
		"proj-a": true,
		"proj-b": true,
	}}
	tracker := New(Config{
		ParkGrace: 20 * time.Millisecond,
		Sessions:  checker,
		Handler:   handler,
	})
	defer tracker.Close()

	releaseA := tracker.ClaimWorkspace("proj-a")
	releaseA()

	releaseB := tracker.ClaimWorkspace("proj-b")
	defer releaseB()

	time.Sleep(30 * time.Millisecond)

	if tracker.IsParked("proj-a") {
		t.Fatal("proj-a was parked despite having active sessions")
	}
	if tracker.IsParked("proj-b") {
		t.Fatal("proj-b was parked despite having active workspace claim")
	}

	handler.mu.Lock()
	parked := len(handler.parked)
	handler.mu.Unlock()
	if parked != 0 {
		t.Fatalf("expected 0 park calls, got %d", parked)
	}
}

func TestReactivateWithinGraceCancelsPark(t *testing.T) {
	handler := &testHandler{}
	checker := &testActivityChecker{hasActive: map[string]bool{"proj-1": false}}
	tracker := New(Config{
		ParkGrace: 30 * time.Millisecond,
		Sessions:  checker,
		Handler:   handler,
	})
	defer tracker.Close()

	release1 := tracker.ClaimWorkspace("proj-1")
	release1()

	time.Sleep(10 * time.Millisecond)
	release2 := tracker.ClaimWorkspace("proj-1")

	time.Sleep(30 * time.Millisecond)
	if tracker.IsParked("proj-1") {
		t.Fatal("expected proj-1 to remain active because it was reclaimed before grace expired")
	}
	release2()
}

func TestClaimTurnBlocksParking(t *testing.T) {
	handler := &testHandler{}
	checker := &testActivityChecker{hasActive: map[string]bool{"proj-1": false}}
	tracker := New(Config{
		ParkGrace: 20 * time.Millisecond,
		Sessions:  checker,
		Handler:   handler,
	})
	defer tracker.Close()

	releaseWorkspace := tracker.ClaimWorkspace("proj-1")
	releaseTurn := tracker.ClaimTurn("proj-1", "turn-1")

	releaseWorkspace()

	time.Sleep(30 * time.Millisecond)
	if tracker.IsParked("proj-1") {
		t.Fatal("expected proj-1 to stay active while turn is running")
	}

	releaseTurn()

	time.Sleep(30 * time.Millisecond)
	if !tracker.IsParked("proj-1") {
		t.Fatal("expected proj-1 to park once turn completes and grace expires")
	}
}

func TestWakeParkedProjectFiresActivate(t *testing.T) {
	handler := &testHandler{}
	checker := &testActivityChecker{hasActive: map[string]bool{"proj-1": false}}
	tracker := New(Config{
		ParkGrace: 10 * time.Millisecond,
		Sessions:  checker,
		Handler:   handler,
	})
	defer tracker.Close()

	release := tracker.ClaimWorkspace("proj-1")
	release()

	time.Sleep(20 * time.Millisecond)
	if !tracker.IsParked("proj-1") {
		t.Fatal("expected proj-1 to be parked")
	}

	releaseWake := tracker.ClaimWorkspace("proj-1")
	defer releaseWake()

	if tracker.IsParked("proj-1") {
		t.Fatal("expected proj-1 to be active after wake claim")
	}

	handler.mu.Lock()
	activated := len(handler.activated)
	handler.mu.Unlock()
	if activated != 1 {
		t.Fatalf("expected 1 activate call on wake, got %d", activated)
	}
}

func TestConcurrentClaimsAreAllCountedAndReleased(t *testing.T) {
	const claims = 20
	handler := &testHandler{}
	tracker := New(Config{
		ParkGrace: time.Hour,
		Sessions:  &testActivityChecker{hasActive: map[string]bool{"proj-1": false}},
		Handler:   handler,
	})
	defer tracker.Close()

	counts := func() (workspace, session int, parkScheduled bool) {
		tracker.mu.Lock()
		defer tracker.mu.Unlock()
		p := tracker.projects["proj-1"]
		return p.workspaceClaims, p.sessions["sess"], p.parkTimer != nil
	}

	var claimed, released sync.WaitGroup
	release := make(chan struct{})
	for i := range claims {
		claimed.Add(1)
		released.Add(1)
		go func() {
			defer released.Done()
			var done func()
			if i%2 == 0 {
				done = tracker.ClaimWorkspace("proj-1")
			} else {
				done = tracker.ClaimSession("proj-1", "sess")
			}
			claimed.Done()
			<-release
			done()
		}()
	}

	claimed.Wait()
	if workspace, session, parkScheduled := counts(); workspace != claims/2 || session != claims/2 || parkScheduled {
		t.Fatalf("held claims: workspace=%d session=%d park scheduled=%v, want %d each and no park", workspace, session, parkScheduled, claims/2)
	}
	if tracker.ParkNow(t.Context(), "proj-1") {
		t.Fatal("parked a project with held workspace claims")
	}

	close(release)
	released.Wait()
	if workspace, session, parkScheduled := counts(); workspace != 0 || session != 0 || !parkScheduled {
		t.Fatalf("released claims: workspace=%d session=%d park scheduled=%v, want none held and a park scheduled", workspace, session, parkScheduled)
	}
	if !tracker.ParkNow(t.Context(), "proj-1") || !tracker.IsParked("proj-1") {
		t.Fatal("project did not park after every claim was released")
	}
	handler.mu.Lock()
	defer handler.mu.Unlock()
	if len(handler.parked) != 1 || len(handler.activated) != 0 {
		t.Fatalf("handler saw parks=%v activations=%v, want one park and no activation", handler.parked, handler.activated)
	}
}
