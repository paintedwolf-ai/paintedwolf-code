package workflow

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/coordinator/batch"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/profiles"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

const hostVarBaselinePosture = "pre_workflow_posture"

func boundaryVisibility(run *api.WorkflowRun) api.MessageVisibility {
	if run != nil && strings.TrimSpace(run.AttachPolicy) == string(workflowdef.AttachPolicySessionCreate) {
		return api.MessageVisibilityInternal
	}
	return api.MessageVisibilityTranscript
}

func newBoundaryMessage(run *api.WorkflowRun, event, phase, reason string) api.Message {
	workflowID, workflowVersion, runID := "", "", ""
	if run != nil {
		workflowID = run.WorkflowID
		workflowVersion = run.WorkflowVersion
		runID = run.ID
	}
	meta := api.WorkflowBoundaryMeta{
		Event:           event,
		WorkflowID:      workflowID,
		WorkflowVersion: workflowVersion,
		Phase:           phase,
		Reason:          reason,
	}
	msg := api.Message{
		ID:               uuid.NewString(),
		Role:             api.MessageRoleSystem,
		Kind:             api.MessageKindWorkflowBoundary,
		Visibility:       boundaryVisibility(run),
		WorkflowBoundary: &meta,
		CreatedAt:        time.Now().UTC(),
	}
	if runID != "" {
		msg.WorkflowRunID = runID
	}
	return msg
}

func startBoundaryMessages(run *api.WorkflowRun, slashText, operationID string) ([]api.Message, api.Message) {
	now := time.Now().UTC()
	msgs := make([]api.Message, 0, 2)
	if text := strings.TrimSpace(slashText); text != "" {
		// The slash row echoes the operation id — the submission id clients
		// reconcile pending sends against.
		msgs = append(msgs, api.Message{
			ID: operationID, Role: api.MessageRoleUser,
			Content: text, WorkflowRunID: run.ID, CreatedAt: now,
		})
	}
	start := newBoundaryMessage(run, "started", run.CurrentPhase, "")
	start.ID = workflowOperationMessageID(operationID, "started")
	start.CreatedAt = now
	msgs = append(msgs, start)
	return msgs, start
}

func (m *RunManager) appendSessionMessages(ctx context.Context, sessionID string, msgs ...api.Message) error {
	if m == nil || m.Sessions == nil {
		return fmt.Errorf("session store not configured")
	}
	if err := m.Sessions.AppendMessages(ctx, sessionID, msgs...); err != nil {
		return err
	}
	m.publishMessageAppends(ctx, sessionID, msgs...)
	return nil
}

func (m *RunManager) publishMessageAppends(ctx context.Context, sessionID string, msgs ...api.Message) {
	if m == nil || m.Events == nil || len(msgs) == 0 {
		return
	}
	if m.Sessions != nil && m.Sessions.MutationEventsOutboxed() {
		return
	}
	key := ""
	if sess, err := m.Sessions.Get(ctx, sessionID); err == nil && sess != nil {
		key = sess.ProjectID
	}
	if key == "" {
		return
	}
	for _, msg := range msgs {
		if msg.ID == "" {
			continue
		}
		m.Events.PublishMessageAppend(ctx, key, sessionID, msg)
	}
}

// publishMessagePatch emits an in-place transcript update.
func (m *RunManager) publishMessagePatch(ctx context.Context, sessionID string, msg api.Message) {
	if m == nil || m.Events == nil || msg.ID == "" {
		return
	}
	if m.Sessions != nil && m.Sessions.MutationEventsOutboxed() {
		return
	}
	key := ""
	if sess, err := m.Sessions.Get(ctx, sessionID); err == nil && sess != nil {
		key = sess.ProjectID
	}
	if key == "" {
		return
	}
	m.Events.PublishMessagePatch(ctx, key, sessionID, msg)
}

func saveBaselinePosture(vars map[string]any, posture api.SessionPosture) map[string]any {
	vars = cloneVars(vars)
	vars = SetHostVar(vars, hostVarBaselinePosture, string(posture))
	return vars
}

func workflowMutationPosture(run *api.WorkflowRun, vars map[string]any, active api.SessionPosture) api.SessionPosture {
	if run != nil && IsTerminal(run.Status) {
		baseline, _ := vars[hostVarBaselinePosture].(string)
		if profiles.ValidSessionPosture(baseline) {
			return api.SessionPosture(baseline)
		}
	}
	return active
}

// StampAndAppendMessages sets workflow_run_id on messages while a run is active.
func (m *RunManager) StampAndAppendMessages(ctx context.Context, sessionID string, msgs ...api.Message) error {
	if m == nil || m.Sessions == nil {
		return fmt.Errorf("session store not configured")
	}
	active, err := m.Store.ActiveBySession(ctx, sessionID)
	if err != nil {
		return err
	}
	for i := range msgs {
		if msgs[i].WorkflowBoundary != nil || msgs[i].Kind == api.MessageKindWorkflowBoundary {
			continue
		}
		if active != nil && strings.TrimSpace(msgs[i].WorkflowRunID) == "" {
			msgs[i].WorkflowRunID = active.ID
		}
	}
	return m.Sessions.AppendMessages(ctx, sessionID, msgs...)
}

// ApplyCoordinatorBatchEvent implements session.WorkflowSessionView.
func (m *RunManager) ApplyCoordinatorBatchEvent(ctx context.Context, sessionID string, ev batch.Event, eventSeq int) error {
	_, err := m.applyCoordinatorBatchEvent(ctx, sessionID, ev, eventSeq)
	return err
}

var _ session.WorkflowSessionView = (*RunManager)(nil)
