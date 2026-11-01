package toolexecution

import (
	"strings"

	"github.com/lycaon/lycaon/internal/hitl"
)

// UserGuidanceKey is the agent-public reject-data key read by policy templates.

// AttachUserGuidance sets the human's direction on a structured reject.
// Empty direction attaches nothing.

func capabilityDenialKey(sessionID, toolCallID, actionID string) string {
	sessionID = strings.TrimSpace(sessionID)
	toolCallID = strings.TrimSpace(toolCallID)
	if sessionID == "" || toolCallID == "" {
		return ""
	}
	if actionID == "" {
		actionID = toolCallID
	}
	return sessionID + "\x00" + toolCallID + "\x00" + actionID
}

// rememberCapabilityDenial carries a checkpoint refusal across callbacks without an error return.
func (e *Rejections) rememberCapabilityDenial(sessionID, toolCallID, actionID string, final *hitl.CheckpointResponse) {
	if e == nil || final == nil || final.Status != hitl.DecisionStatusRejected {
		return
	}
	key := capabilityDenialKey(sessionID, toolCallID, actionID)
	if key == "" {
		return
	}
	e.capabilityDenials.Store(key, struct{}{})
}

// takeCapabilityDenial consumes the refusal for this invocation; checkpoint results carry its guidance.
func (e *Rejections) takeCapabilityDenial(sessionID, toolCallID, actionID string) bool {
	if e == nil {
		return false
	}
	key := capabilityDenialKey(sessionID, toolCallID, actionID)
	if key == "" {
		return false
	}
	_, ok := e.capabilityDenials.LoadAndDelete(key)
	return ok
}
