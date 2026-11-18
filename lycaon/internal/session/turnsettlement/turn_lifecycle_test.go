package turnsettlement

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/promptresult"
	"github.com/lycaon/lycaon/internal/session/batchcontrol"
	"github.com/lycaon/lycaon/internal/session/execution"
	"github.com/lycaon/lycaon/internal/session/promptstate"
	"github.com/lycaon/lycaon/internal/session/store/storetest"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type stubGate struct{ inProgress bool }

func (g stubGate) InProgress(context.Context, string) bool { return g.inProgress }

type stubWorkflowPhases struct{}

func (stubWorkflowPhases) ReconcileTurnCompletion(context.Context, string) error { return nil }

type stubWorkflowReports struct{}

func (stubWorkflowReports) MaybeDeliverTopologyReport(context.Context, string, string) error {
	return nil
}

type stubWorkflowPolicy struct{}

func (stubWorkflowPolicy) AssertSessionCompletion(context.Context, string) error {
	return errors.New("workflow incomplete")
}
func (stubWorkflowPolicy) CurrentPhase(context.Context, string) string { return "plan" }

func TestFinishAndSettlePendingPreservesCloseoutBlockedWithoutFailingTurn(t *testing.T) {
	sessionStore := storetest.New(t, "proj-1")
	sess, err := sessionStore.Create(t.Context(), api.CreateSessionRequest{}, "proj-1")
	testutil.FailErr(t, "create session", err)
	testutil.FailErr(t, "mark session busy", sessionStore.SetSessionStatus(t.Context(), sess.ID, api.SessionStatusBusy))

	status := execution.NewStatus(sessionStore)
	status.SetPublisher(&events.Publisher{Hub: events.NewMemoryHub()})
	rt := coordinator.NewRuntime(coordinator.RuntimeDeps{})
	svc := New(sessionStore, stubGate{}, &promptstate.MutexRegistry{}, status, nil, &batchcontrol.Service{})
	svc.SetRuntime(rt)
	svc.SetWorkflow(&WorkflowDomains{
		Phases:  stubWorkflowPhases{},
		Policy:  stubWorkflowPolicy{},
		Reports: stubWorkflowReports{},
	})

	svc.Begin(sess.ID)
	blocked := &promptresult.CloseoutBlocked{
		Code:           "WORKFLOW_GATES_OPEN_BEFORE_CLOSEOUT",
		Phase:          "plan",
		OpenGateLeaves: []string{"plan_approved"},
		Notice:         "Workflow phase plan is waiting on open gate(s): plan_approved (WORKFLOW_GATES_OPEN_BEFORE_CLOSEOUT).",
	}
	svc.NoteCloseoutBlocked(sess.ID, blocked)
	testutil.FailErr(t, "finish closeout_blocked turn", svc.Finish(t.Context(), sess.ID, false, false, ""))
	if !svc.Pending(sess.ID) {
		t.Fatal("expected deferred closeout_blocked settlement")
	}
	testutil.FailErr(t, "settle pending closeout_blocked turn", svc.SettlePending(t.Context(), sess.ID))
	updated, err := sessionStore.Get(t.Context(), sess.ID)
	testutil.FailErr(t, "read settled session", err)
	if updated.Status != api.SessionStatusIdle {
		t.Fatalf("session status = %s, want idle", updated.Status)
	}
}
