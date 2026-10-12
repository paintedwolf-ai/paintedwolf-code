package hitl

import (
	"github.com/lycaon/lycaon/pkg/api"
)

func (m *ApprovalAuthority) notifyToolApprovalTerminal(row StoredCheckpoint, status DecisionStatus) {
	if m == nil || m.onToolApprovalTerminal == nil || row.Kind != api.CheckpointKindToolApproval {
		return
	}
	key := stringField(row.Payload, "coalesce_grant_key")
	if key == "" {
		return
	}
	chat := stringField(row.Payload, "coalesce_chat")
	if chat == "" {
		chat = row.SessionID
	}
	m.onToolApprovalTerminal(chat, key, status)
}

func (m *ApprovalAuthority) SetToolApprovalTerminalHook(fn func(chatSessionID, grantKey string, status DecisionStatus)) {
	if m == nil {
		return
	}
	m.onToolApprovalTerminal = fn
}

func (m *ApprovalAuthority) SetToolApprovalRestoreHook(fn func(StoredCheckpoint)) {
	if m == nil {
		return
	}
	m.onToolApprovalRestored = fn
}

func (m *ApprovalAuthority) restoreToolApproval(row StoredCheckpoint) {
	if row.Kind == api.CheckpointKindToolApproval && m.onToolApprovalRestored != nil {
		m.onToolApprovalRestored(row)
	}
}
