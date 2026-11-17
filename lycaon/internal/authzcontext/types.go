// Package authzcontext persists tamper-evident authorization context rows
// (effective grant set sealed at run-start) and supports hash-chain verification.
package authzcontext

import (
	"time"

	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/settings"
)

// Hash versions select the chain payload a row's digest covers.
const (
	// HashVersion1 is the events digest, and the contexts digest that also
	// covers the worker ceiling those rows recorded.
	HashVersion1 = 1
	// HashVersion2 is the contexts digest new contexts are sealed with.
	HashVersion2 = 2
)

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
	NeverAsk       bool
	MCPInventory   MCPInventory
	SpawnAllowlist []string
	// V1Ceiling is the worker ceiling a hash-version-1 row recorded and its digest covers.
	V1Ceiling  *V1Ceiling
	ConfigHash string
	SealedAt   time.Time
}

// MCPInventory captures loaded MCP provider ids and the mcp_* tool surface at seal time.
type MCPInventory struct {
	ProviderIDs []string `json:"provider_ids"`
	Tools       []string `json:"tools"`
}

// V1Ceiling is the worker tool-round ceiling hash-version-1 rows recorded.
type V1Ceiling struct {
	MaxToolLoops int
	Default      int
	Min          int
	Max          int
}
