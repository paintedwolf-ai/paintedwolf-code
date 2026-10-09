package session

import (
	"context"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
	"testing"
)

type pendingOutcomeQueue struct {
	stubWorkerCycleLister
	pending []api.WorkerTask
}

func (q *pendingOutcomeQueue) ListPendingOutcomes(context.Context) ([]api.WorkerTask, error) {
	return q.pending, nil
}

func TestWorkerSynthesisSettlesAfterOutcomeAcknowledgement(t *testing.T) {
	ctx := t.Context()
	st := store.NewMemory()
	mgr := NewManager(st, nil, tools.NewStubRegistry(), settings.DefaultSessionLimits())
	sess, err := st.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	testutil.FailErr(t, "mark visible turn busy", st.SetSessionStatus(ctx, sess.ID, api.SessionStatusBusy))
	q := &pendingOutcomeQueue{pending: []api.WorkerTask{{
		ID: "job-a", ProjectID: sess.ProjectID, ParentSessionID: sess.ID, Status: api.WorkerStatusComplete,
	}}}
	mgr.SetWorkerQueue(q)
	loop := mgr.ensureCoordinatorRuntime().CoordinatorLoop()
	finishExecution := loop.Admission.BeginPromptExecution(t.Context(), sess.ID)
	defer finishExecution()
	loop.Nudges.Nudge(ctx, sess.ID, anchor.PhaseAdvanced, anchor.PhaseAdvanced, "", anchor.Envelope{})
	loop.Cycles.OnWorkerCycleTerminal(ctx, sess.ID, "job-a")
	if !loop.Nudges.HasPendingLoopWakes(sess.ID) {
		t.Fatal("phase wake must wait for outcome acknowledgement")
	}

	q.pending = nil
	mgr.beginPromptTurn(sess.ID, "")
	testutil.FailErr(t, "finish worker synthesis", mgr.finishPromptExecution(ctx, sess.ID, false, true, ""))
	finishExecution()
	testutil.FailErr(t, "drain worker synthesis wakes", mgr.drainPendingLoopWakes(ctx, sess.ID))
	settled, err := st.Get(ctx, sess.ID)
	testutil.FailErr(t, "read completed session", err)
	if settled.Status != api.SessionStatusIdle {
		t.Fatalf("status after acknowledged worker synthesis = %q want idle", settled.Status)
	}
	if loop.Nudges.HasPendingLoopWakes(sess.ID) {
		t.Fatal("completed worker synthesis retained a deferred wake")
	}
}
