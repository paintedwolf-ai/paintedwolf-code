package toolhost

import (
	"github.com/lycaon/lycaon/internal/session/approvalstate"
	"github.com/lycaon/lycaon/internal/tools"
)

// toolApprovalCoalesceAdapter adapts approvalstate.ToolApprovalCoalesce to tools.ToolApprovalCoalesce
// (named Begin result types differ across packages).
type toolApprovalCoalesceAdapter struct {
	rt *approvalstate.ToolApprovalCoalesce
}

func (a toolApprovalCoalesceAdapter) Begin(chatSessionID, grantKey string) (tools.ToolApprovalCoalesceBegin, string) {
	b, id := a.rt.Begin(chatSessionID, grantKey)
	return tools.ToolApprovalCoalesceBegin(b), id
}

func (a toolApprovalCoalesceAdapter) RegisterPending(chatSessionID, grantKey, checkpointID, toolCallID string) {
	a.rt.RegisterPending(chatSessionID, grantKey, checkpointID, toolCallID)
}

func (a toolApprovalCoalesceAdapter) AbortMint(chatSessionID, grantKey string) {
	a.rt.AbortMint(chatSessionID, grantKey)
}

func (a toolApprovalCoalesceAdapter) ClearPending(chatSessionID, grantKey string) {
	a.rt.ClearPending(chatSessionID, grantKey)
}

func (a toolApprovalCoalesceAdapter) RecordDeny(chatSessionID, grantKey string) {
	a.rt.RecordDeny(chatSessionID, grantKey)
}

func (a toolApprovalCoalesceAdapter) ClearDeny(chatSessionID, grantKey string) {
	a.rt.ClearDeny(chatSessionID, grantKey)
}

func (a toolApprovalCoalesceAdapter) NoteJoin(chatSessionID, grantKey, toolCallID string) int {
	return a.rt.NoteJoin(chatSessionID, grantKey, toolCallID)
}

func (a toolApprovalCoalesceAdapter) JoinedCount(chatSessionID, grantKey string) int {
	return a.rt.JoinedCount(chatSessionID, grantKey)
}

func (a toolApprovalCoalesceAdapter) JoinedToolCallIDs(chatSessionID, grantKey string) []string {
	return a.rt.JoinedToolCallIDs(chatSessionID, grantKey)
}
