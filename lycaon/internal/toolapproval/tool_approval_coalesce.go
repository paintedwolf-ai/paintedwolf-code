package toolapproval

// ToolApprovalCoalesceBegin is the outcome of consulting pending-approval spam guards.
type ToolApprovalCoalesceBegin int

const (
	// ToolApprovalCoalesceMint — RequestCheckpoint then RegisterPending.
	ToolApprovalCoalesceMint ToolApprovalCoalesceBegin = iota
	// ToolApprovalCoalesceJoin — wait on an existing checkpoint; do not mint.
	ToolApprovalCoalesceJoin
	// ToolApprovalCoalesceSkipDenied — grantKey is in the deny-set; fail the tool.
	ToolApprovalCoalesceSkipDenied
)

// ToolApprovalCoalesce shares pending reviews across matching invocations.
type ToolApprovalCoalesce interface {
	Begin(chatSessionID, grantKey string) (ToolApprovalCoalesceBegin, string)
	RegisterPending(chatSessionID, grantKey, checkpointID, toolCallID string)
	AbortMint(chatSessionID, grantKey string)
	ClearPending(chatSessionID, grantKey string)
	RecordDeny(chatSessionID, grantKey string)
	ClearDeny(chatSessionID, grantKey string)
	NoteJoin(chatSessionID, grantKey, toolCallID string) int
	JoinedCount(chatSessionID, grantKey string) int
	JoinedToolCallIDs(chatSessionID, grantKey string) []string
}
