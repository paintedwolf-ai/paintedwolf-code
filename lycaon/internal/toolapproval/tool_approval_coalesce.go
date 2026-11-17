package toolapproval

import "github.com/lycaon/lycaon/internal/session/approvalstate"

// ToolApprovalCoalesce shares pending reviews across matching invocations.
type ToolApprovalCoalesce interface {
	Begin(chatSessionID, grantKey string) (approvalstate.ToolApprovalCoalesceBegin, string)
	RegisterPending(chatSessionID, grantKey, checkpointID, toolCallID string)
	AbortMint(chatSessionID, grantKey string)
	ClearPending(chatSessionID, grantKey string)
	NoteJoin(chatSessionID, grantKey, toolCallID string) int
	JoinedCount(chatSessionID, grantKey string) int
	JoinedToolCallIDs(chatSessionID, grantKey string) []string
}
