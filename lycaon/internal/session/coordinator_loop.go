package session

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/internal/coordinator/loopwake"
	"github.com/lycaon/lycaon/pkg/api"
)

// SetLoopWorkflowSource wires workflow run lookups for coordinator loop policy.
func (m *Host) SetLoopWorkflowSource(src *loopwake.WorkflowDomains) {
	if m == nil {
		return
	}
	m.Coordinator.Loop.Workflow = src
	m.Coordinator.Control.Approvals = nil
	m.Coordinator.Control.Obligations = nil
	if src != nil {
		m.Coordinator.Control.Approvals = src.Approvals
		m.Coordinator.Control.Obligations = src.Obligations
	}
	m.Runner.Settlement.SetWorkflowSource(src)
}

// appendLoopMessages persists coordinator rows, then hands each durable review
// result to the workflows subsystem so repair accounting follows the transcript.
func appendLoopMessages(m *Host, ctx context.Context, sessionID string, msgs ...api.Message) error {
	if m == nil {
		return fmt.Errorf("session host unavailable")
	}
	if err := m.Runner.Transcript.Append(ctx, sessionID, msgs...); err != nil {
		return err
	}
	if m.Coordinator.Projection == nil || m.Coordinator.Projection.Reviews == nil {
		return nil
	}
	for _, msg := range msgs {
		if err := m.Coordinator.Projection.Reviews.RecordReviewToolResult(ctx, sessionID, msg); err != nil {
			return err
		}
	}
	return nil
}
