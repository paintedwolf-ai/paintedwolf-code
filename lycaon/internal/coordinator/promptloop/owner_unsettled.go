package promptloop

import (
	"errors"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

// ErrOwnerUnsettled ends a worker child after a TOOL_OWNER_FAILED batch.
var ErrOwnerUnsettled = errors.New("worker tool subsystem owner unsettled")

func batchHasUnsettledOwner(history []api.Message, assistantMessageID string) bool {
	assistantMessageID = strings.TrimSpace(assistantMessageID)
	for _, msg := range history {
		if msg.Role != api.MessageRoleTool || msg.ToolResult == nil {
			continue
		}
		if assistantMessageID != "" && strings.TrimSpace(msg.ToolResult.AssistantMessageID) != assistantMessageID {
			continue
		}
		if resultHasOwnerFailed(msg.ToolResult) {
			return true
		}
	}
	return false
}

func resultHasOwnerFailed(tr *api.ToolResult) bool {
	if tr == nil || tr.Outcome != api.ToolResultOutcomeError {
		return false
	}
	for _, code := range tr.Codes {
		if strings.TrimSpace(code) == toolrejection.ToolOwnerFailedCode {
			return true
		}
	}
	if tr.Invocation != nil && tr.Invocation.Failure != nil {
		return strings.TrimSpace(tr.Invocation.Failure.Code) == toolrejection.ToolOwnerFailedCode &&
			strings.TrimSpace(tr.Invocation.Failure.Class) == "owner_error"
	}
	return false
}
