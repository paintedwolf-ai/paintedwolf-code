// Package authzcontext persists tamper-evident authorization context rows
// (effective grant set sealed at run-start) and supports hash-chain verification.
package authzcontext

import (
	"time"

	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/spawn"
)

const HashVersion1 = 1

// Context is the durable effective-approvals grant set for one sealed row.
type Context struct {
	ID              string
	SessionID       string
	ContextSeq      int
	PrevHash        string
	RowHash         string
	HashVersion     int
	WorkerJobID     string
	ParentSessionID string
	ProjectID       string
	AgentType       string
	ToolProfile     string
	Posture         string
	ApprovalPosture gate.Posture
	AllowedTools    []string
	DenyTools       []string
	MCPDeny         []string
	ReadGlobs       []string
	WriteGlobs      []string
	AskRules        []settings.ApprovalRule
	// Grants is the turn's reusable authority snapshot.
	Grants []settings.ApprovalGrant
	// ChatGrants is the chat-scoped reusable authority live when the context was sealed.
	ChatGrants []hitl.ApprovalGrant
	// NeverAsk is the merged prompt-disable state.
	NeverAsk         bool
	MaxToolLoops     int
	MCPInventory     MCPInventory
	SpawnAllowlist   []string
	WorkerToolBudget spawn.WorkerToolBudget
	ConfigHash       string
	SealedAt         time.Time
}

// MCPInventory captures loaded MCP provider ids and the mcp_* tool surface at seal time.
type MCPInventory struct {
	ProviderIDs []string `json:"provider_ids"`
	Tools       []string `json:"tools"`
}
