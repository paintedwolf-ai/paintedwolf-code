package runstate

import (
	"fmt"
	"strings"
	"time"

	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

// CoordinatorAskVar stores the durable coordinator ask.
const CoordinatorAskVar = "coordinator_ask"

type CoordinatorAskState string

const (
	CoordinatorAskPending    CoordinatorAskState = "pending"
	CoordinatorAskAnswered   CoordinatorAskState = "answered"
	CoordinatorAskCanceled   CoordinatorAskState = "canceled"
	CoordinatorAskSuperseded CoordinatorAskState = "superseded"
)

// CoordinatorAsk is the durable ask record.
type CoordinatorAsk struct {
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
	State          CoordinatorAskState              `json:"state"`
	Response       string                           `json:"response,omitempty"`
	Choices        []string                         `json:"choices,omitempty"`
	ResolvedBy     string                           `json:"resolved_by,omitempty"`
	// ResolvedByPersonID names the person who answered.
	ResolvedByPersonID string     `json:"resolved_by_person_id,omitempty"`
	ClosedReason       string     `json:"closed_reason,omitempty"`
	CreatedAt          time.Time  `json:"created_at"`
	AnsweredAt         *time.Time `json:"answered_at,omitempty"`
}

func CoordinatorAskFromVars(vars map[string]any) (CoordinatorAsk, bool) {
	raw, ok := vars[CoordinatorAskVar].(map[string]any)
	if !ok || raw == nil {
		return CoordinatorAsk{}, false
	}
	ask := CoordinatorAsk{
		ID: strings.TrimSpace(StringValue(raw["id"])), RunID: strings.TrimSpace(StringValue(raw["run_id"])),
		Prompt: strings.TrimSpace(StringValue(raw["prompt"])), ResponseType: workflowdef.FeedbackResponseType(strings.TrimSpace(StringValue(raw["response_type"]))),
		Options: CleanAskOptions(StringSlice(raw["options"])), AllowOther: BoolValue(raw["allow_other"]),
		ArtifactID: strings.TrimSpace(StringValue(raw["artifact_id"])), ArtifactIDs: CleanAskOptions(StringSlice(raw["artifact_ids"])),
		Purpose: strings.TrimSpace(StringValue(raw["purpose"])), ToolCallID: strings.TrimSpace(StringValue(raw["tool_call_id"])),
		InputDigest: strings.TrimSpace(StringValue(raw["input_digest"])), State: CoordinatorAskState(strings.TrimSpace(StringValue(raw["state"]))),
		Response: strings.TrimSpace(StringValue(raw["response"])), Choices: CleanAskOptions(StringSlice(raw["choices"])),
		ResolvedBy: strings.TrimSpace(StringValue(raw["resolved_by"])), ResolvedByPersonID: strings.TrimSpace(StringValue(raw["resolved_by_person_id"])),
		ClosedReason: strings.TrimSpace(StringValue(raw["closed_reason"])),
	}
	if secret, ok := raw["secret"].(map[string]any); ok {
		ask.Secret = &workflowdef.SecretInputSpec{
			Name: strings.TrimSpace(StringValue(secret["name"])), Purpose: strings.TrimSpace(StringValue(secret["purpose"])),
			Scope: strings.TrimSpace(StringValue(secret["scope"])),
		}
		switch n := secret["agent_use_ttl_seconds"].(type) {
		case float64:
			ask.Secret.AgentUseTTLSeconds = int64(n)
		case int64:
			ask.Secret.AgentUseTTLSeconds = n
		}
	}
	if ask.ID == "" || ask.RunID == "" || ask.Prompt == "" || ask.State == "" {
		return CoordinatorAsk{}, false
	}
	if v, ok := raw["issued_revision"].(float64); ok {
		ask.IssuedRevision = int64(v)
	} else if v, ok := raw["issued_revision"].(int64); ok {
		ask.IssuedRevision = v
	}
	if rawCreated := strings.TrimSpace(StringValue(raw["created_at"])); rawCreated != "" {
		ask.CreatedAt, _ = time.Parse(time.RFC3339Nano, rawCreated)
	}
	if rawAnswered := strings.TrimSpace(StringValue(raw["answered_at"])); rawAnswered != "" {
		if answeredAt, err := time.Parse(time.RFC3339Nano, rawAnswered); err == nil {
			ask.AnsweredAt = &answeredAt
		}
	}
	return ask, true
}

func SetCoordinatorAsk(vars map[string]any, ask CoordinatorAsk) map[string]any {
	vars = CloneVars(vars)
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
	vars[CoordinatorAskVar] = record
	return vars
}

func CoordinatorAskPendingFromVars(vars map[string]any) (CoordinatorAsk, bool) {
	ask, ok := CoordinatorAskFromVars(vars)
	return ask, ok && ask.State == CoordinatorAskPending
}

func PendingCoordinatorAskAPI(vars map[string]any) (api.PendingFeedback, bool) {
	ask, ok := CoordinatorAskPendingFromVars(vars)
	if !ok {
		return api.PendingFeedback{}, false
	}
	return api.PendingFeedback{
		PhaseID: ask.ID, Prompt: ask.Prompt, ResponseType: string(ask.ResponseType),
		Options: append([]string(nil), ask.Options...), AllowOther: ask.AllowOther,
		ArtifactID: ask.ArtifactID, ArtifactIDs: append([]string(nil), ask.ArtifactIDs...), Purpose: ask.Purpose,
		Secret:         SecretInputMeta(ask.Secret),
		IssuedRevision: ask.IssuedRevision,
	}, true
}

func ValidateCoordinatorAskForResolution(run *api.WorkflowRun, ask CoordinatorAsk, requestID string) error {
	if ask.State != CoordinatorAskPending || ask.ID != strings.TrimSpace(requestID) {
		return ErrFeedbackNotPending
	}
	if run == nil || run.Status != api.WorkflowRunStatusRunning || ask.RunID != run.ID {
		return fmt.Errorf("%w: ask %s is stale", ErrRevisionConflict, ask.ID)
	}
	return nil
}

func StringValue(v any) string { s, _ := v.(string); return s }
func BoolValue(v any) bool     { b, _ := v.(bool); return b }

func StringSlice(v any) []string {
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

func CleanAskOptions(options []string) []string {
	out := make([]string, 0, len(options))
	seen := make(map[string]struct{}, len(options))
	for _, o := range options {
		if o = strings.TrimSpace(o); o != "" {
			if _, exists := seen[o]; exists {
				continue
			}
			seen[o] = struct{}{}
			out = append(out, o)
		}
	}
	return out
}
