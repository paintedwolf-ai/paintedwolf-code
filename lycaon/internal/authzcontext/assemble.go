package authzcontext

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/pkg/api"
)

// AssembleInput is the machine state available at run-start seal.
type AssembleInput struct {
	Session          *api.Session
	ProfileID        string
	Profile          sandbox.ToolProfile
	Perms            settings.ApprovalConfig
	ChatGrants       []hitl.ApprovalGrant
	WorkerJobID      string
	MCPInventory     MCPInventory
	SpawnAllowlist   []string
	WorkerToolBudget spawn.WorkerToolBudget
}

// Assemble builds a Context from resolved run-start state.
func Assemble(in AssembleInput) Context {
	now := time.Now().UTC()
	var c Context
	c.ID = uuid.NewString()
	c.SealedAt = now
	c.HashVersion = HashVersion1
	if sess := in.Session; sess != nil {
		c.SessionID = strings.TrimSpace(sess.ID)
		c.ParentSessionID = strings.TrimSpace(sess.ParentSessionID)
		c.ProjectID = strings.TrimSpace(sess.ProjectID)
		c.AgentType = strings.TrimSpace(sess.AgentType)
		c.Posture = string(sess.Posture)
		c.MaxToolLoops = sess.MaxToolLoops
	}
	c.WorkerJobID = strings.TrimSpace(in.WorkerJobID)
	c.ToolProfile = strings.TrimSpace(in.ProfileID)
	if c.ToolProfile == "" {
		c.ToolProfile = strings.TrimSpace(in.Profile.ID)
	}
	c.ApprovalPosture = in.Perms.Posture
	c.AllowedTools = sortedKeys(in.Profile.Tools)
	c.DenyTools = sortedCopy(in.Profile.DenyTools)
	c.MCPDeny = sortedCopy(in.Profile.MCPDeny)
	c.ReadGlobs = sortedCopy(in.Profile.ReadGlobs)
	c.WriteGlobs = sortedCopy(in.Profile.WriteGlobs)
	c.AskRules = append([]settings.ApprovalRule(nil), in.Perms.Rules...)
	c.Grants = sortedGrants(in.Perms.Grants)
	c.ChatGrants = sortedChatGrants(in.ChatGrants)
	c.NeverAsk = in.Perms.NeverAsk != nil && *in.Perms.NeverAsk
	c.MCPInventory = MCPInventory{
		ProviderIDs: sortedCopy(in.MCPInventory.ProviderIDs),
		Tools:       sortedCopy(in.MCPInventory.Tools),
	}
	c.SpawnAllowlist = sortedCopy(in.SpawnAllowlist)
	c.WorkerToolBudget = in.WorkerToolBudget
	c.ConfigHash = configHash(c)
	return c
}

// MCPToolsFromProfile returns sorted mcp_* tool names enabled on profile.
func MCPToolsFromProfile(profile sandbox.ToolProfile) []string {
	if len(profile.Tools) == 0 {
		return nil
	}
	out := make([]string, 0)
	for name, ok := range profile.Tools {
		if !ok || !strings.HasPrefix(name, "mcp_") {
			continue
		}
		out = append(out, name)
	}
	sort.Strings(out)
	if len(out) == 0 {
		return nil
	}
	return out
}

func sortedKeys(m map[string]bool) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k, ok := range m {
		if ok {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func sortedCopy(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

func sortedGrants(in []settings.ApprovalGrant) []settings.ApprovalGrant {
	if len(in) == 0 {
		return nil
	}
	out := append([]settings.ApprovalGrant(nil), in...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func sortedChatGrants(in []hitl.ApprovalGrant) []hitl.ApprovalGrant {
	if len(in) == 0 {
		return nil
	}
	out := append([]hitl.ApprovalGrant(nil), in...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

type hashPayload struct {
	ToolProfile      string                   `json:"tool_profile"`
	ApprovalPosture  gate.Posture             `json:"approval_posture"`
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
}

func configHash(c Context) string {
	payload := hashPayload{
		ToolProfile:      c.ToolProfile,
		ApprovalPosture:  c.ApprovalPosture,
		AllowedTools:     c.AllowedTools,
		DenyTools:        c.DenyTools,
		MCPDeny:          c.MCPDeny,
		ReadGlobs:        c.ReadGlobs,
		WriteGlobs:       c.WriteGlobs,
		AskRules:         c.AskRules,
		Grants:           c.Grants,
		ChatGrants:       c.ChatGrants,
		NeverAsk:         c.NeverAsk,
		MaxToolLoops:     c.MaxToolLoops,
		MCPInventory:     c.MCPInventory,
		SpawnAllowlist:   c.SpawnAllowlist,
		WorkerToolBudget: spawnToolBudgetToWire(c.WorkerToolBudget),
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:16])
}

// ProfileMap indexes tool profiles by id.
func ProfileMap(profiles []sandbox.ToolProfile) map[string]sandbox.ToolProfile {
	if len(profiles) == 0 {
		return nil
	}
	out := make(map[string]sandbox.ToolProfile, len(profiles))
	for _, p := range profiles {
		out[p.ID] = p
	}
	return out
}

// ApprovalConfigForSession resolves merged approval rules for a session workspace.
func ApprovalConfigForSession(perms *settings.ApprovalStore, sess *api.Session) settings.ApprovalConfig {
	if perms == nil || sess == nil {
		return settings.ApprovalConfig{}
	}
	return perms.Get(llm.SettingsScopeProject, settings.ProjectRef{
		ID:  strings.TrimSpace(sess.ProjectID),
		Dir: strings.TrimSpace(sess.WorkspacePath),
	})
}
