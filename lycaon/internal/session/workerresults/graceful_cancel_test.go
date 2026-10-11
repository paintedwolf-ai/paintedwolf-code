package workerresults

import (
	"context"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func newGracefulCancelTestManager(t *testing.T) *GracefulCancel {
	t.Helper()
	return NewGracefulCancel()
}

func TestRegisterNonBlocking(t *testing.T) {
	mgr := newGracefulCancelTestManager(t)
	child := &api.Session{ID: "child-1", ParentSessionID: "parent-1"}

	start := time.Now()
	if err := mgr.Register(child.ID, "job-1", "user-canceled"); err != nil {
		testutil.FailErr(t, "Register", err)
	}
	if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
		t.Fatalf("Register blocked for %v; must be non-blocking", elapsed)
	}
	reason, pending := mgr.Pending(context.Background(), child)
	if !pending {
		t.Fatal("expected registration pending after Register")
	}
	if reason != "user-canceled" {
		t.Fatalf("reason = %q want user-canceled", reason)
	}
}

func TestRegisterOutlivesRequest(t *testing.T) {
	mgr := newGracefulCancelTestManager(t)
	child := &api.Session{ID: "child-2", ParentSessionID: "parent-1"}

	reqCtx, reqCancel := context.WithCancel(context.Background())
	if err := mgr.Register(child.ID, "job-2", "aborted-request"); err != nil {
		testutil.FailErr(t, "Register", err)
	}
	// The registration takes no request context, so canceling the request leaves it pending.
	if _, pending := mgr.Pending(reqCtx, child); !pending {
		t.Fatal("expected registration pending before request cancel")
	}
	reqCancel()
	time.Sleep(10 * time.Millisecond) // allow the cancellation to propagate

	if _, pending := mgr.Pending(context.Background(), child); !pending {
		t.Fatal("registration cleared by request cancellation; register-and-return must outlive the request")
	}
}

func TestFinishConsumesRegistrationNoWaiter(t *testing.T) {
	mgr := newGracefulCancelTestManager(t)
	child := &api.Session{ID: "child-3", ParentSessionID: "parent-1"}

	if err := mgr.Register(child.ID, "job-3", "closeout"); err != nil {
		testutil.FailErr(t, "Register", err)
	}
	// With no waiter, Finish returns promptly and reclaims the map entry.
	done := make(chan struct{})
	go func() {
		mgr.Finish(child.ID)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Finish blocked")
	}
	if _, pending := mgr.Pending(context.Background(), child); pending {
		t.Fatal("Finish did not consume the registration")
	}
}

func TestRegisterValidatesArgs(t *testing.T) {
	mgr := newGracefulCancelTestManager(t)
	if err := mgr.Register("", "job", "r"); err == nil {
		t.Fatal("expected error for missing child session")
	}
	if err := mgr.Register("child", "", "r"); err == nil {
		t.Fatal("expected error for missing job id")
	}
}
