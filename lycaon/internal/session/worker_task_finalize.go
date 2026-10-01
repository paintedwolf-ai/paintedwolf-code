package session

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/pkg/api"
)

// HostTaskCallPrefix marks host-created worker cards.
const HostTaskCallPrefix = "task:"

// WorkerDispatchRowInput is the enqueue identity for a parent-chat worker card.
type WorkerDispatchRowInput struct {
	JobID          string
	AgentType      string
	Brief          string
	ChildSessionID string
	ToolCallID     string
}

type workerDispatchPair struct {
	Assistant api.Message
	Tool      api.Message
}

// EnsureWorkerCardProjection records a missing transcript projection for a worker job.
func (m *Manager) EnsureWorkerCardProjection(ctx context.Context, parentID string, in WorkerDispatchRowInput) error {
	if m == nil || m.store == nil {
		return nil
	}
	parentID = strings.TrimSpace(parentID)
	jobID := strings.TrimSpace(in.JobID)
	if parentID == "" || jobID == "" {
		return nil
	}
	msgs, err := m.store.GetMessages(ctx, parentID)
	if err != nil {
		return err
	}
	if _, ok := EnqueuedTaskToolMessageForJob(msgs, jobID); ok {
		return nil
	}
	if assistantOwnsToolCall(msgs, in.ToolCallID) {
		return nil
	}
	pair, err := workerDispatchPairFrom(in)
	if err != nil {
		return err
	}
	return m.appendMessages(ctx, parentID, pair.Assistant, pair.Tool)
}

func workerDispatchPairFrom(in WorkerDispatchRowInput) (workerDispatchPair, error) {
	jobID := strings.TrimSpace(in.JobID)
	assistantID := uuid.NewString()
	toolCallID := strings.TrimSpace(in.ToolCallID)
	if toolCallID == "" {
		toolCallID = HostTaskCallPrefix + jobID
	}
	agentType := strings.TrimSpace(in.AgentType)
	brief := strings.TrimSpace(in.Brief)
	toolArgs := map[string]any{"job_id": jobID}
	if agentType != "" {
		toolArgs["agent_type"] = agentType
	}
	if brief != "" {
		toolArgs["brief"] = map[string]any{
			"goal":      brief,
			"done_when": []string{"Return the completed assignment."},
		}
	}
	payload := map[string]any{
		"job_id": jobID,
		"status": "enqueued",
	}
	if agentType != "" {
		payload["agent_type"] = agentType
	}
	if child := strings.TrimSpace(in.ChildSessionID); child != "" {
		payload["child_session_id"] = child
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return workerDispatchPair{}, fmt.Errorf("encode worker dispatch: %w", err)
	}
	content := string(raw)
	tr := guidance.ComposeToolResult(content, guidance.ToolResultFacts{
		Outcome: api.ToolResultOutcomeCompleted,
	}, nil)
	if tr == nil {
		tr = &api.ToolResult{Content: content}
	}
	guidance.ApplyTaskDispatchMetadata("task", tr, &api.WorkerDispatch{
		WorkerID: jobID, ChildSessionID: in.ChildSessionID, AgentType: agentType,
	})
	tr.Tool = "task"
	tr.ToolCallID = toolCallID
	tr.AssistantMessageID = assistantID
	tr.ToolArgs = toolArgs
	return workerDispatchPair{
		Assistant: api.Message{
			ID:        assistantID,
			Role:      api.MessageRoleAssistant,
			Origin:    api.MessageOriginHost,
			Authority: api.ContentAuthorityNone,
			TrustTier: api.ContentTrustTierTrusted,
			Content:   "",
			ToolCalls: []api.ToolCall{{
				Name: "task",
				ID:   toolCallID,
				Args: toolArgs,
			}},
		},
		Tool: api.Message{
			ID:         uuid.NewString(),
			Role:       api.MessageRoleTool,
			Origin:     api.MessageOriginTool,
			Authority:  api.ContentAuthorityNone,
			TrustTier:  api.ContentTrustTierUntrusted,
			Content:    content,
			ToolResult: tr,
		},
	}, nil
}

// ProjectWorkerCard writes a committed worker result to its transcript projection.
func (m *Manager) ProjectWorkerCard(ctx context.Context, parentID, jobID string, ws *api.WorkerSummaryMeta) error {
	if m == nil || m.store == nil {
		return fmt.Errorf("session manager unavailable")
	}
	if ws == nil {
		return fmt.Errorf("worker summary required")
	}
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return fmt.Errorf("worker summary job_id required")
	}
	if err := validateWorkerSummary(jobID, ws); err != nil {
		return err
	}
	msgs, err := m.store.GetMessages(ctx, parentID)
	if err != nil {
		return err
	}
	if _, ok := EnqueuedTaskToolMessageForJob(msgs, jobID); !ok {
		in := WorkerDispatchRowInput{JobID: jobID, AgentType: ws.AgentType}
		if m.workerQueue != nil {
			if task, found := m.workerQueue.Get(jobID); found && task != nil {
				in.AgentType = task.AgentType
				in.Brief = task.Brief
				in.ChildSessionID = task.ChildSessionID
				// Terminal delivery retries until the original tool response is persisted.
				in.ToolCallID = task.SourceToolCallID
			}
		}
		if err := m.EnsureWorkerCardProjection(ctx, parentID, in); err != nil {
			return err
		}
		msgs, err = m.store.GetMessages(ctx, parentID)
		if err != nil {
			return err
		}
	}
	toolMsg, ok := EnqueuedTaskToolMessageForJob(msgs, jobID)
	if !ok {
		return fmt.Errorf("worker card projection missing for job %s", jobID)
	}
	toolMsg.WorkerSummary = ws
	toolMsg.SourceContext = ws.SourceContext
	promoteWorkerEnvelopeToBody(toolMsg, ws.Envelope)
	return m.updateMessage(ctx, parentID, toolMsg.ID, *toolMsg)
}

// promoteWorkerEnvelopeToBody replaces dispatch content with completion content.
func promoteWorkerEnvelopeToBody(msg *api.Message, envelope string) {
	envelope = strings.TrimSpace(envelope)
	if msg == nil || envelope == "" {
		return
	}
	msg.Content = envelope
	msg.ContentParts = nil
	if msg.ToolResult != nil {
		msg.ToolResult.Content = envelope
	}
	// Preserve completion structure during context compaction.
	msg.DietStamp = compaction.DietStampPreserveStructure
	msg.DietStampSource = compaction.DietStampSourceWorkerEnvelope
}

func validateWorkerSummary(jobID string, ws *api.WorkerSummaryMeta) error {
	ws.DelegationID = strings.TrimSpace(ws.DelegationID)
	ws.LegID = strings.TrimSpace(ws.LegID)
	ws.WorkerID = strings.TrimSpace(ws.WorkerID)
	ws.ChildSessionID = strings.TrimSpace(ws.ChildSessionID)
	ws.AgentType = strings.TrimSpace(ws.AgentType)
	ws.Status = api.WorkerSummaryStatus(strings.TrimSpace(string(ws.Status)))
	ws.Envelope = strings.TrimSpace(ws.Envelope)
	if ws.WorkerID == "" || ws.AgentType == "" || ws.Status == "" || ws.Envelope == "" {
		return fmt.Errorf("worker summary identity, status, and envelope required")
	}
	if ws.ChildSessionID == "" && ws.Status != api.WorkerSummaryStatusCanceled && ws.Status != api.WorkerSummaryStatusHeld {
		return fmt.Errorf("worker summary child_session_id required")
	}
	if ws.WorkerID != jobID {
		return fmt.Errorf("worker summary job_id %q does not match %q", ws.WorkerID, jobID)
	}
	env, ok := ParseWorkerCompletionEnvelope(ws.Envelope)
	if !ok {
		return fmt.Errorf("worker summary envelope invalid")
	}
	if env.JobID != ws.WorkerID || env.ChildSessionID != ws.ChildSessionID || env.AgentType != ws.AgentType || workercompletion.NormalizeWorkerCompletionState(env.State) != workercompletion.NormalizeWorkerCompletionState(string(ws.Status)) {
		return fmt.Errorf("worker summary envelope identity mismatch")
	}
	return nil
}

// EnqueuedTaskToolMessageForJob finds a job's canonical dispatch row.
func EnqueuedTaskToolMessageForJob(msgs []api.Message, jobID string) (*api.Message, bool) {
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return nil, false
	}
	for i := range msgs {
		msg := &msgs[i]
		if !isCanonicalTaskDispatchRow(msg) {
			continue
		}
		if strings.TrimSpace(msg.ToolResult.Dispatch.WorkerID) == jobID {
			return msg, true
		}
	}
	return nil, false
}

func assistantOwnsToolCall(msgs []api.Message, toolCallID string) bool {
	toolCallID = strings.TrimSpace(toolCallID)
	if toolCallID == "" {
		return false
	}
	for i := range msgs {
		if msgs[i].Role != api.MessageRoleAssistant {
			continue
		}
		for _, call := range msgs[i].ToolCalls {
			if strings.TrimSpace(call.ID) == toolCallID {
				return true
			}
		}
	}
	return false
}

func isCanonicalTaskDispatchRow(msg *api.Message) bool {
	if msg == nil || msg.Role != api.MessageRoleTool || msg.ToolResult == nil || msg.ToolResult.Dispatch == nil {
		return false
	}
	if strings.TrimSpace(msg.ToolResult.Dispatch.WorkerID) == "" {
		return false
	}
	switch strings.TrimSpace(strings.ToLower(msg.ToolResult.Tool)) {
	case "task", "delegate_dispatch":
		return true
	default:
		return false
	}
}
