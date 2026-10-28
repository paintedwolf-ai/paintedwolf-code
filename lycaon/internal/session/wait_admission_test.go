package session

import (
	"context"
	"runtime"
	"testing"

	"github.com/google/uuid"
	awaitstore "github.com/lycaon/lycaon/internal/await"
	"github.com/lycaon/lycaon/internal/coordinator/loopwake"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestWaitResumeAcquiresItsOwnDispatchLane(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		name := "pending"
		if canceled {
			name = "interrupted"
		}
		t.Run(name, func(t *testing.T) {
			manager, sessions := newReceiptTestManager(t)
			session := newReceiptTestSession(t, manager, sessions)
			ctx, release := manager.Submissions.LockDispatch(t.Context(), session.ID)
			defer func() { release() }()
			started := make(chan struct{})
			admitted := make(chan struct{}, 1)
			finished := make(chan error, 1)
			leaseID := uuid.NewString()
			pending := true
			go func() {
				close(started)
				_, err := manager.Submissions.WaitResume(context.WithoutCancel(ctx), session.ID, loopwake.WaitDelivery{
					LeaseID: leaseID, Condition: awaitstore.Condition{Kind: "timer", Outcome: "timed_out"},
					Pending: func() bool { return pending }, Admitted: func() error { admitted <- struct{}{}; return nil },
				})
				finished <- err
			}()
			<-started
			runtime.Gosched()
			select {
			case <-admitted:
				t.Error("wait delivery inherited another execution's dispatch ownership")
			default:
			}
			pending = !canceled
			release()
			release = func() {}
			select {
			case err := <-finished:
				testutil.FailErr(t, "deliver wait after dispatch release", err)
			case <-t.Context().Done():
				t.Fatal("wait delivery did not acquire the released lane")
			}
			ids := sessions.admitted()
			if canceled && len(ids) != 0 {
				t.Fatalf("interrupted wait admitted receipts: %v", ids)
			}
			if !canceled && (len(ids) != 1 || ids[0] != leaseID) {
				t.Fatalf("wait admission identities = %v", ids)
			}
		})
	}
}
