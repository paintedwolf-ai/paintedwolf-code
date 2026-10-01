package approvals

import (
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/ingestion"
	"github.com/lycaon/lycaon/internal/settings"
)

const (
	// Interaction keys for bounded explanation shapes (not one entry per command).
	KeyCommand             = "command"
	KeyCommandDestructive  = "command_destructive"
	KeyMCPCall             = "mcp_call"
	KeyPathOutsideProject  = "path_outside_project"
	KeyOutboundSecret      = "outbound_secret"
	FallbackExplanationKey = "_unknown"
)

// GateableKeys returns sorted explanation keys that can raise tool_approval checkpoints.
func GateableKeys() []string {
	seen := map[string]struct{}{}
	add := func(keys ...string) {
		for _, k := range keys {
			seen[k] = struct{}{}
		}
	}
	for _, tool := range settings.ApprovalRecoverableToolIDs() {
		if tool == "command" {
			continue
		}
		add(tool)
	}
	add(settings.ApprovalIrreversibleToolIDs()...)
	add(KeyCommand, KeyCommandDestructive, KeyMCPCall, KeyPathOutsideProject, KeyOutboundSecret, FallbackExplanationKey)
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ResolveExplanationKey picks the registry entry for an action and tier.
func ResolveExplanationKey(action hitl.ProposedAction, tier settings.ReversibilityTier) string {
	if settings.PathEscapesWorkspace(action) {
		return KeyPathOutsideProject
	}
	tool := strings.TrimSpace(action.Tool)
	if settings.IsCommandToolName(tool) {
		if tier == settings.TierIrreversible {
			return KeyCommandDestructive
		}
		return KeyCommand
	}
	if ingestion.IsMCPToolName(tool) {
		return KeyMCPCall
	}
	if tool != "" {
		return tool
	}
	return FallbackExplanationKey
}

// ActionTemplateVars builds approval template data.
func ActionTemplateVars(action hitl.ProposedAction) map[string]any {
	vars := map[string]any{
		"tool":         strings.TrimSpace(action.Tool),
		"project":      strings.TrimSpace(action.ProjectDir),
		"command":      strings.TrimSpace(action.Command),
		"action_label": strings.TrimSpace(action.Tool),
	}
	if action.ApprovalCategory == "mcp" && strings.TrimSpace(action.ApprovalSubject) != "" {
		vars["action_label"] = strings.TrimSpace(action.ApprovalSubject)
	}
	if vars["command"] == "" {
		vars["command"] = settings.CommandTextFromActionArgs(action.Args)
	}
	if action.Args != nil {
		for _, k := range []string{"remote", "branch", "provider", "surface", "surface_label", "destination_label", "destination_kind", "rule_id", "rule_title", "host"} {
			if v, ok := action.Args[k]; ok {
				vars[k] = v
			}
		}
	}
	if len(action.Files) > 0 {
		vars["path"] = action.Files[0]
	}
	return vars
}
