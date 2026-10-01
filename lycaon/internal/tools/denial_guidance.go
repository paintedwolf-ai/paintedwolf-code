package tools

import (
	"strings"

	"github.com/lycaon/lycaon/internal/hitl"
)

// UserGuidanceKey is the agent-public reject-data key read by policy templates.
const UserGuidanceKey = "user_guidance"

// AttachUserGuidance sets the human's direction on a structured reject.
// Empty direction attaches nothing.
func AttachUserGuidance(reject *ToolReject, guidance string) {
	guidance = strings.TrimSpace(guidance)
	if reject == nil || guidance == "" {
		return
	}
	if reject.Data == nil {
		reject.Data = map[string]any{}
	}
	reject.Data[UserGuidanceKey] = guidance
}

func capabilityDenialKey(sessionID, toolCallID string) string {
	sessionID = strings.TrimSpace(sessionID)
	toolCallID = strings.TrimSpace(toolCallID)
	if sessionID == "" || toolCallID == "" {
		return ""
	}
	return sessionID + "\x00" + toolCallID
}

// rememberCapabilityDenial carries a checkpoint refusal across callbacks without an error return.
func (e *DefaultToolExecutor) rememberCapabilityDenial(sessionID, toolCallID string, final *hitl.CheckpointResponse) {
	if e == nil || final == nil || final.Status != hitl.DecisionStatusRejected {
		return
	}
	key := capabilityDenialKey(sessionID, toolCallID)
	if key == "" {
		return
	}
	e.capabilityDenials.Store(key, struct{}{})
}

// takeCapabilityDenial consumes the refusal for this invocation; checkpoint results carry its guidance.
func (e *DefaultToolExecutor) takeCapabilityDenial(sessionID, toolCallID string) bool {
	if e == nil {
		return false
	}
	key := capabilityDenialKey(sessionID, toolCallID)
	if key == "" {
		return false
	}
	_, ok := e.capabilityDenials.LoadAndDelete(key)
	return ok
}
