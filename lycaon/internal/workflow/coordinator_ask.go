package workflow

import (
	"context"
	"fmt"
	"strings"
	"time"

	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

// coordinatorAskVar stores the durable coordinator ask.
const coordinatorAskVar = "coordinator_ask"

type coordinatorAskState string

const (
	coordinatorAskPending    coordinatorAskState = "pending"
	coordinatorAskAnswered   coordinatorAskState = "answered"
	coordinatorAskCanceled   coordinatorAskState = "canceled"
	coordinatorAskSuperseded coordinatorAskState = "superseded"
)

// coordinatorAsk is the durable ask record.
type coordinatorAsk struct {
	ID             string                           `json:"id"`
	RunID          string                           `json:"run_id"`
	IssuedRevision int64                            `json:"issued_revision"`
	Prompt         string                           `json:"prompt"`
	ResponseType   workflowdef.FeedbackResponseType `json:"response_type"`
	Options        []string                         `json:"options,omitempty"`
	AllowOther     bool                             `json:"allow_other"`
	ArtifactID     string                           `json:"artifact_id,omitempty"`
	ArtifactIDs    []string                         `json:"artifact_ids,omitempty"`
	Purpose        string                           `json:"purpose"`
	ToolCallID     string                           `json:"tool_call_id"`
	InputDigest    string                           `json:"input_digest"`
	Secret         *workflowdef.SecretInputSpec     `json:"secret,omitempty"`
	State          coordinatorAskState              `json:"state"`
	Response       string                           `json:"response,omitempty"`
	Choices        []string                         `json:"choices,omitempty"`
	ResolvedBy     string                           `json:"resolved_by,omitempty"`
	// ResolvedByPersonID names the person who answered.
	ResolvedByPersonID string     `json:"resolved_by_person_id,omitempty"`
	ClosedReason       string     `json:"closed_reason,omitempty"`
	CreatedAt          time.Time  `json:"created_at"`
	AnsweredAt         *time.Time `json:"answered_at,omitempty"`
}

func coordinatorAskFromVars(vars map[string]any) (coordinatorAsk, bool) {
	raw, ok := vars[coordinatorAskVar].(map[string]any)
	if !ok || raw == nil {
		return coordinatorAsk{}, false
	}
	ask := coordinatorAsk{
		ID: strings.TrimSpace(stringValue(raw["id"])), RunID: strings.TrimSpace(stringValue(raw["run_id"])),
		Prompt: strings.TrimSpace(stringValue(raw["prompt"])), ResponseType: workflowdef.FeedbackResponseType(strings.TrimSpace(stringValue(raw["response_type"]))),
		Options: cleanAskOptions(stringSlice(raw["options"])), AllowOther: boolValue(raw["allow_other"]),
		ArtifactID: strings.TrimSpace(stringValue(raw["artifact_id"])), ArtifactIDs: cleanAskOptions(stringSlice(raw["artifact_ids"])),
		Purpose: strings.TrimSpace(stringValue(raw["purpose"])), ToolCallID: strings.TrimSpace(stringValue(raw["tool_call_id"])),
		InputDigest: strings.TrimSpace(stringValue(raw["input_digest"])), State: coordinatorAskState(strings.TrimSpace(stringValue(raw["state"]))),
		Response: strings.TrimSpace(stringValue(raw["response"])), Choices: cleanAskOptions(stringSlice(raw["choices"])),
		ResolvedBy: strings.TrimSpace(stringValue(raw["resolved_by"])), ResolvedByPersonID: strings.TrimSpace(stringValue(raw["resolved_by_person_id"])),
		ClosedReason: strings.TrimSpace(stringValue(raw["closed_reason"])),
	}
	if secret, ok := raw["secret"].(map[string]any); ok {
		ask.Secret = &workflowdef.SecretInputSpec{
			Name: strings.TrimSpace(stringValue(secret["name"])), Purpose: strings.TrimSpace(stringValue(secret["purpose"])),
			Scope: strings.TrimSpace(stringValue(secret["scope"])),
		}
		switch n := secret["agent_use_ttl_seconds"].(type) {
		case float64:
			ask.Secret.AgentUseTTLSeconds = int64(n)
		case int64:
			ask.Secret.AgentUseTTLSeconds = n
		}
	}
	if ask.ID == "" || ask.RunID == "" || ask.Prompt == "" || ask.State == "" {
		return coordinatorAsk{}, false
	}
	if v, ok := raw["issued_revision"].(float64); ok {
		ask.IssuedRevision = int64(v)
	} else if v, ok := raw["issued_revision"].(int64); ok {
		ask.IssuedRevision = v
	}
	if rawCreated := strings.TrimSpace(stringValue(raw["created_at"])); rawCreated != "" {
		ask.CreatedAt, _ = time.Parse(time.RFC3339Nano, rawCreated)
	}
	if rawAnswered := strings.TrimSpace(stringValue(raw["answered_at"])); rawAnswered != "" {
		if answeredAt, err := time.Parse(time.RFC3339Nano, rawAnswered); err == nil {
			ask.AnsweredAt = &answeredAt
		}
	}
	return ask, true
}

func setCoordinatorAsk(vars map[string]any, ask coordinatorAsk) map[string]any {
	vars = cloneVars(vars)
	record := map[string]any{
		"id": ask.ID, "run_id": ask.RunID, "issued_revision": ask.IssuedRevision,
		"prompt": ask.Prompt, "response_type": string(ask.ResponseType), "options": append([]string(nil), ask.Options...),
		"allow_other": ask.AllowOther, "artifact_id": ask.ArtifactID, "artifact_ids": append([]string(nil), ask.ArtifactIDs...),
		"purpose": ask.Purpose, "tool_call_id": ask.ToolCallID, "input_digest": ask.InputDigest,
		"state": string(ask.State), "response": ask.Response, "choices": append([]string(nil), ask.Choices...),
		"resolved_by": ask.ResolvedBy, "resolved_by_person_id": ask.ResolvedByPersonID, "closed_reason": ask.ClosedReason,
		"created_at": ask.CreatedAt.UTC().Format(time.RFC3339Nano),
	}
	if ask.Secret != nil {
		record["secret"] = map[string]any{
			"name": ask.Secret.Name, "purpose": ask.Secret.Purpose, "scope": ask.Secret.Scope,
			"agent_use_ttl_seconds": ask.Secret.AgentUseTTLSeconds,
		}
	}
	if ask.AnsweredAt != nil {
		record["answered_at"] = ask.AnsweredAt.UTC().Format(time.RFC3339Nano)
	}
	vars[coordinatorAskVar] = record
	return vars
}

func coordinatorAskPendingFromVars(vars map[string]any) (coordinatorAsk, bool) {
	ask, ok := coordinatorAskFromVars(vars)
	return ask, ok && ask.State == coordinatorAskPending
}

func pendingCoordinatorAskAPI(vars map[string]any) (api.PendingFeedback, bool) {
	ask, ok := coordinatorAskPendingFromVars(vars)
	if !ok {
		return api.PendingFeedback{}, false
	}
	return api.PendingFeedback{
		PhaseID: ask.ID, Prompt: ask.Prompt, ResponseType: string(ask.ResponseType),
		Options: append([]string(nil), ask.Options...), AllowOther: ask.AllowOther,
		ArtifactID: ask.ArtifactID, ArtifactIDs: append([]string(nil), ask.ArtifactIDs...), Purpose: ask.Purpose,
		Secret:         apiSecretInput(ask.Secret),
		IssuedRevision: ask.IssuedRevision,
	}, true
}

func apiSecretInput(secret *workflowdef.SecretInputSpec) *api.SecretInputMeta {
	if secret == nil {
		return nil
	}
	return &api.SecretInputMeta{
		Name: secret.Name, Purpose: secret.Purpose, Scope: secret.Scope,
		AgentUseTTLMs: secret.AgentUseTTLSeconds * 1000,
	}
}

func validateCoordinatorAskForResolution(run *api.WorkflowRun, ask coordinatorAsk, requestID string) error {
	if ask.State != coordinatorAskPending || ask.ID != strings.TrimSpace(requestID) {
		return ErrFeedbackNotPending
	}
	if run == nil || run.Status != api.WorkflowRunStatusRunning || ask.RunID != run.ID {
		return fmt.Errorf("%w: ask %s is stale", ErrRunRevisionConflict, ask.ID)
	}
	return nil
}

func stringValue(v any) string { s, _ := v.(string); return s }
func boolValue(v any) bool     { b, _ := v.(bool); return b }

func stringSlice(v any) []string {
	switch items := v.(type) {
	case []string:
		return append([]string(nil), items...)
	case []any:
		out := make([]string, 0, len(items))
		for _, item := range items {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

// reconcileCoordinatorAskProjection restores ask transcript projections.
func (m *RunManager) reconcileCoordinatorAskProjection(ctx context.Context, sessionID string, vars map[string]any) {
	if m == nil || m.Sessions == nil {
		return
	}
	ask, ok := coordinatorAskFromVars(vars)
	if !ok || (ask.State != coordinatorAskPending && ask.State != coordinatorAskAnswered) {
		return
	}
	run, err := m.Store.Get(ctx, ask.RunID)
	if err != nil || run == nil {
		return
	}
	fb := &workflowdef.UserFeedbackPrompt{
		Prompt: ask.Prompt, ResponseType: ask.ResponseType, Options: append([]string(nil), ask.Options...),
		AllowOther: ask.AllowOther, ArtifactID: ask.ArtifactID, ArtifactIDs: append([]string(nil), ask.ArtifactIDs...), Purpose: ask.Purpose,
		Secret: ask.Secret,
	}
	_, card := buildFeedbackAnnouncement(run, ask.ID, fb, vars, workflowAnnouncementMessageID(run.ID, ask.ID))
	if card != nil {
		msgs, messagesErr := m.Sessions.GetMessages(ctx, sessionID)
		if messagesErr == nil {
			found := false
			for _, msg := range msgs {
				if msg.ID == card.ID {
					found = true
					break
				}
			}
			if !found {
				m.appendAnnouncement(ctx, sessionID, card)
			}
		}
	}
	if ask.State == coordinatorAskAnswered {
		m.persistCoordinatorAskAnswer(ctx, sessionID, ask)
		m.stampFeedbackAnswer(ctx, sessionID, ask.RunID, ask.ID, ask.Response, ask.ResolvedByPersonID)
	}
}
