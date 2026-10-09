package session

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/pkg/api"
)

const workflowGatesOpenCode = "WORKFLOW_GATES_OPEN_BEFORE_CLOSEOUT"

const closeoutGateDelayMaxPerPrompt = 2

// Gate repair is offered only when the coordinator can invoke tools.
func (m *Manager) maybeRejectCloseoutForOpenGates(ctx context.Context, sess *api.Session, workersIdle bool, invokeAllowed bool) (*guidance.Refusal, bool) {
	if m == nil || sess == nil || m.workflows == nil {
		return nil, false
	}
	if !invokeAllowed {
		return nil, false
	}
	state := m.workflows.Policy.ActiveCloseoutGateState(ctx, sess.ID)
	if !state.Gated || len(state.OpenLeaves) == 0 {
		return nil, false
	}
	root := RootSessionID(ctx, m.store, sess.ID)
	leaves := strings.Join(state.OpenLeaves, ", ")
	delayCount, delayed := m.closeout.delay(sess.ID, root, closeoutGateDelay, workersIdle, closeoutGateDelayMaxPerPrompt)
	return m.tryOARFinishBlock(ctx, sess, func(gc *oar.GuardContext) error {
		gc.WorkersIdle = workersIdle
		gc.Phase = state.Phase
		gc.CloseoutGatesOpen = true
		gc.CloseoutGateOpenLeaves = leaves
		gc.CloseoutGateDelayCount = int64(delayCount)
		if delayed {
			gc.PutRejectData(workflowGatesOpenCode, map[string]any{
				"phase":                     state.Phase,
				"closeout_gate_open_leaves": leaves,
			})
		}
		return nil
	})
}
