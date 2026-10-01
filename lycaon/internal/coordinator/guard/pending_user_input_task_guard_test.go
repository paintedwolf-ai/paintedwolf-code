package guard_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/guard"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestObserveTaskWhilePendingUserInput(t *testing.T) {
	sess := &api.Session{ID: "s1"}

	gc := oar.NewGuardContext()
	guard.ObserveTaskWhilePendingUserInput(sess, surface.ImplementSessionState{}, "task", gc)
	if observeHasCode(gc, guard.PendingUserInputTaskForbiddenCode) {
		t.Fatal("task must be allowed when PendingUserInput is false")
	}

	gc = oar.NewGuardContext()
	guard.ObserveTaskWhilePendingUserInput(
		sess,
		surface.ImplementSessionState{PendingUserInput: true},
		"wait",
		gc,
	)
	if observeHasCode(gc, guard.PendingUserInputTaskForbiddenCode) {
		t.Fatal("wait must not trip the pending-user-input task guard")
	}

	gc = oar.NewGuardContext()
	guard.ObserveTaskWhilePendingUserInput(
		&api.Session{ID: "child", ParentSessionID: "s1"},
		surface.ImplementSessionState{PendingUserInput: true},
		"task",
		gc,
	)
	if observeHasCode(gc, guard.PendingUserInputTaskForbiddenCode) {
		t.Fatal("worker child sessions must not trip coordinator task guards")
	}

	gc = oar.NewGuardContext()
	guard.ObserveTaskWhilePendingUserInput(
		sess,
		surface.ImplementSessionState{PendingUserInput: true},
		"task",
		gc,
	)
	if !observeHasCode(gc, guard.PendingUserInputTaskForbiddenCode) {
		t.Fatalf("want %s in %v", guard.PendingUserInputTaskForbiddenCode, gc.ArgValidationErrors)
	}
}
