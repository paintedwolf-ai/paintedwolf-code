package turnguards

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/session/closeouts"
	sessiontree "github.com/lycaon/lycaon/internal/session/tree"
	"github.com/lycaon/lycaon/pkg/api"
)

const workflowGatesOpenCode = "WORKFLOW_GATES_OPEN_BEFORE_CLOSEOUT"
const closeoutGateDelayMaxPerPrompt = 2

const overlayDispositionRequiredCode = "OVERLAY_DISPOSITION_REQUIRED_BEFORE_CLOSEOUT"

// PendingOverlays keeps execution completion separate from delivery to the primary tree.
func (m *Service) PendingOverlays(ctx context.Context, sess *api.Session, invokeAllowed bool) (*guidance.Refusal, bool) {
	if m == nil || sess == nil || sess.IsWorkerChild() {
		return nil, false
	}
	pending := m.state.ForSession(ctx, sess).PendingOverlayIDs
	if len(pending) == 0 {
		return nil, false
	}
	reject, blocked := m.ToolPolicy.FinishBlock(ctx, sess, func(gc *oar.GuardContext) error {
		gc.Workers.PendingOverlayPromote = true
		gc.Rejection.RejectObservation = "closeout_pending_overlays"
		gc.PutRejectData(overlayDispositionRequiredCode, m.pendingResultData(ctx, pending[0]))
		return nil
	})
	if blocked && !invokeAllowed {
		reject = reject.WithCause(guidance.ErrCloseoutStopped)
	}
	return reject, blocked
}

// OpenGates bounds repair attempts independently of completion admission.
func (m *Service) OpenGates(ctx context.Context, sess *api.Session, workersIdle, invokeAllowed bool) (*guidance.Refusal, bool) {
	if m == nil || sess == nil || sess.IsWorkerChild() || m.workflows == nil {
		return nil, false
	}
	state, err := m.workflows.Policy.ActiveCloseoutGateState(ctx, sess.ID)
	if err != nil {
		return guidance.NewRefusal("", err.Error()).WithCause(errors.Join(guidance.ErrCloseoutStopped, err)), true
	}
	if !state.Gated || len(state.OpenLeaves) == 0 || !workersIdle {
		return nil, false
	}
	root := sessiontree.RootID(ctx, m.store, sess.ID)
	leaves := strings.Join(state.OpenLeaves, ", ")
	count, retry := m.closeouts.Delay(sess.ID, root, closeouts.GateDelay, invokeAllowed, closeoutGateDelayMaxPerPrompt)
	reject, blocked := m.ToolPolicy.FinishBlock(ctx, sess, func(gc *oar.GuardContext) error {
		gc.Workers.WorkersIdle = workersIdle
		gc.Session.Phase = state.Phase
		gc.Workflow.CloseoutGatesOpen = true
		gc.Workflow.CloseoutGateOpenLeaves = leaves
		gc.PutRejectData(workflowGatesOpenCode, map[string]any{"phase": state.Phase, "closeout_gate_open_leaves": leaves})
		return nil
	})
	if blocked && !retry {
		cause := guidance.ErrCloseoutStopped
		if !invokeAllowed && count == 0 {
			cause = errors.Join(cause, guidance.ErrCloseoutNeedsRepairTurn)
		}
		reject = reject.WithCause(cause)
	}
	return reject, blocked
}

// AssertCompletion also covers host-assembled and forced closeouts.
func (m *Service) AssertCompletion(ctx context.Context, sess *api.Session) error {
	if m == nil || sess == nil || sess.IsWorkerChild() {
		return nil
	}
	if reject, blocked := m.PendingOverlays(ctx, sess, false); blocked {
		return reject
	}
	idle := m.state.ForSession(ctx, sess).WorkersInFlight == 0
	reject, blocked := m.OpenGates(ctx, sess, idle, false)
	if blocked {
		return reject
	}
	if !idle || m.workflows == nil {
		return nil
	}
	return m.workflows.Policy.AssertSessionCompletion(ctx, sess.ID)
}

// pendingResultData names a pending job by its worker and result handle.
func (m *Service) pendingResultData(ctx context.Context, jobID string) map[string]any {
	data := map[string]any{"job_id": jobID}
	if m.jobNames == nil {
		return data
	}
	names, ok, err := m.jobNames.NamesByJob(ctx, jobID)
	if err != nil {
		slog.WarnContext(ctx, "name a pending result for closeout", "job_id", jobID, "error", err)
		return data
	}
	if ok {
		data["worker"] = names.Worker
		if names.Handle != "" {
			data["overlay_id"] = names.Handle
		}
	}
	return data
}
