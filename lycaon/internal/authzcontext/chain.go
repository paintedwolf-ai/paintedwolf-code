package authzcontext

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/jsonvalue"
	"github.com/lycaon/lycaon/internal/settings"
)

type chainPayload struct {
	WorkerJobID     string                   `json:"worker_job_id"`
	ParentSessionID string                   `json:"parent_session_id"`
	ProjectID       string                   `json:"project_id"`
	AgentType       string                   `json:"agent_type"`
	ToolProfile     string                   `json:"tool_profile"`
	Posture         string                   `json:"posture"`
	ApprovalPosture string                   `json:"approval_posture"`
	AllowedTools    []string                 `json:"allowed_tools"`
	DenyTools       []string                 `json:"deny_tools"`
	MCPDeny         []string                 `json:"mcp_deny"`
	ReadGlobs       []string                 `json:"read_globs"`
	WriteGlobs      []string                 `json:"write_globs"`
	AskRules        []settings.ApprovalRule  `json:"ask_rules"`
	Grants          []settings.ApprovalGrant `json:"grants"`
	ChatGrants      []hitl.ApprovalGrant     `json:"chat_grants"`
	NeverAsk        bool                     `json:"never_ask"`
	MCPInventory    MCPInventory             `json:"mcp_inventory"`
	SpawnAllowlist  []string                 `json:"spawn_allowlist"`
	ConfigHash      string                   `json:"config_hash"`
}

// chainPayloadV1 is the hash-version-1 payload: the current fields plus the
// worker ceiling those rows recorded.
type chainPayloadV1 struct {
	chainPayload
	MaxToolLoops     int                 `json:"max_tool_loops"`
	WorkerToolBudget spawnToolBudgetWire `json:"worker_tool_budget"`
}

type spawnToolBudgetWire struct {
	Default int `json:"default"`
	Min     int `json:"min"`
	Max     int `json:"max"`
}

func currentChainPayload(c Context) chainPayload {
	return chainPayload{
		WorkerJobID:     c.WorkerJobID,
		ParentSessionID: c.ParentSessionID,
		ProjectID:       c.ProjectID,
		AgentType:       c.AgentType,
		ToolProfile:     c.ToolProfile,
		Posture:         c.Posture,
		ApprovalPosture: string(c.ApprovalPosture),
		AllowedTools:    append([]string(nil), c.AllowedTools...),
		DenyTools:       append([]string(nil), c.DenyTools...),
		MCPDeny:         append([]string(nil), c.MCPDeny...),
		ReadGlobs:       append([]string(nil), c.ReadGlobs...),
		WriteGlobs:      append([]string(nil), c.WriteGlobs...),
		AskRules:        append([]settings.ApprovalRule(nil), c.AskRules...),
		Grants:          append([]settings.ApprovalGrant(nil), c.Grants...),
		ChatGrants:      append([]hitl.ApprovalGrant(nil), c.ChatGrants...),
		NeverAsk:        c.NeverAsk,
		MCPInventory:    c.MCPInventory,
		SpawnAllowlist:  append([]string(nil), c.SpawnAllowlist...),
		ConfigHash:      c.ConfigHash,
	}
}

// ComputeRowHash implements D2 row_hash for every hash version.
func ComputeRowHash(hashVersion int, sessionID string, seq int, ts string, payload any, prevHash string) (string, error) {
	canonical, err := jsonvalue.CanonicalJSON(payload)
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

// chainPayloadFromContext returns the payload the row's hash version covers.
func chainPayloadFromContext(c Context) any {
	payload := currentChainPayload(c)
	if c.HashVersion != HashVersion1 {
		return payload
	}
	v1 := chainPayloadV1{chainPayload: payload}
	if ceiling := c.V1Ceiling; ceiling != nil {
		v1.MaxToolLoops = ceiling.MaxToolLoops
		v1.WorkerToolBudget = spawnToolBudgetWire{Default: ceiling.Default, Min: ceiling.Min, Max: ceiling.Max}
	}
	return v1
}
