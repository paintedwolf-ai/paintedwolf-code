package authzcontext

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/spawn"
)

type chainPayload struct {
	WorkerJobID      string                   `json:"worker_job_id"`
	ParentSessionID  string                   `json:"parent_session_id"`
	ProjectID        string                   `json:"project_id"`
	AgentType        string                   `json:"agent_type"`
	ToolProfile      string                   `json:"tool_profile"`
	Posture          string                   `json:"posture"`
	ApprovalPosture  string                   `json:"approval_posture"`
	AllowedTools     []string                 `json:"allowed_tools"`
	DenyTools        []string                 `json:"deny_tools"`
	MCPDeny          []string                 `json:"mcp_deny"`
	ReadGlobs        []string                 `json:"read_globs"`
	WriteGlobs       []string                 `json:"write_globs"`
	AskRules         []settings.ApprovalRule  `json:"ask_rules"`
	Grants           []settings.ApprovalGrant `json:"grants"`
	ChatGrants       []hitl.ApprovalGrant     `json:"chat_grants"`
	NeverAsk         bool                     `json:"never_ask"`
	MaxToolLoops     int                      `json:"max_tool_loops"`
	MCPInventory     MCPInventory             `json:"mcp_inventory"`
	SpawnAllowlist   []string                 `json:"spawn_allowlist"`
	WorkerToolBudget spawnToolBudgetWire      `json:"worker_tool_budget"`
	ConfigHash       string                   `json:"config_hash"`
}

type spawnToolBudgetWire struct {
	Default int `json:"default"`
	Min     int `json:"min"`
	Max     int `json:"max"`
}

func spawnToolBudgetToWire(b spawn.WorkerToolBudget) spawnToolBudgetWire {
	return spawnToolBudgetWire{Default: b.Default, Min: b.Min, Max: b.Max}
}

func chainPayloadFromContext(c Context) chainPayload {
	return chainPayload{
		WorkerJobID:      c.WorkerJobID,
		ParentSessionID:  c.ParentSessionID,
		ProjectID:        c.ProjectID,
		AgentType:        c.AgentType,
		ToolProfile:      c.ToolProfile,
		Posture:          c.Posture,
		ApprovalPosture:  string(c.ApprovalPosture),
		AllowedTools:     append([]string(nil), c.AllowedTools...),
		DenyTools:        append([]string(nil), c.DenyTools...),
		MCPDeny:          append([]string(nil), c.MCPDeny...),
		ReadGlobs:        append([]string(nil), c.ReadGlobs...),
		WriteGlobs:       append([]string(nil), c.WriteGlobs...),
		AskRules:         append([]settings.ApprovalRule(nil), c.AskRules...),
		Grants:           append([]settings.ApprovalGrant(nil), c.Grants...),
		ChatGrants:       append([]hitl.ApprovalGrant(nil), c.ChatGrants...),
		NeverAsk:         c.NeverAsk,
		MaxToolLoops:     c.MaxToolLoops,
		MCPInventory:     c.MCPInventory,
		SpawnAllowlist:   append([]string(nil), c.SpawnAllowlist...),
		WorkerToolBudget: spawnToolBudgetToWire(c.WorkerToolBudget),
		ConfigHash:       c.ConfigHash,
	}
}

// ComputeRowHash implements D2 row_hash for hash_version=1.
func ComputeRowHash(hashVersion int, sessionID string, seq int, ts string, payload chainPayload, prevHash string) (string, error) {
	canonical, err := CanonicalJSON(payload)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "%d\n", hashVersion)
	h.Write([]byte(sessionID))
	h.Write([]byte{'\n'})
	h.Write([]byte(strconv.Itoa(seq)))
	h.Write([]byte{'\n'})
	h.Write([]byte(ts))
	h.Write([]byte{'\n'})
	h.Write(canonical)
	h.Write([]byte{'\n'})
	h.Write([]byte(prevHash))
	return hex.EncodeToString(h.Sum(nil)), nil
}

func chainTimestamp(c Context) string {
	if c.SealedAt.IsZero() {
		return ""
	}
	return db.FormatTime(c.SealedAt)
}
