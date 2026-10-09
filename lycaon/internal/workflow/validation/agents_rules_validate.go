package validation

import (
	"fmt"
	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/workflowdiag"
	"github.com/lycaon/lycaon/pkg/api"
	"os"
	"path/filepath"
	"strings"
)

// ValidateAllowedAgents checks every allowed_agents id exists in the agent registry.
func ValidateAllowedAgents(agents orchestration.AgentRegistry, allowed []string) []api.ComposeValidationError {
	if len(allowed) == 0 || agents == nil {
		return nil
	}
	roster := map[string]struct{}{}
	for _, p := range agents.List() {
		roster[p.ID] = struct{}{}
	}
	var out []api.ComposeValidationError
	for i, id := range allowed {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := roster[id]; !ok {
			out = append(out, workflowdiag.EmitDefault(workflowdiag.MustCode("unknown_agent"),
				fmt.Sprintf("allowed_agents[%d]", i),
				map[string]any{"agent": id}))
		}
	}
	return out
}

// ValidateRulesPaths checks rules: entries resolve under bundled embed, checkout, or project overlay.
func ValidateRulesPaths(moduleRoot, projectDir string, rules []string) []api.ComposeValidationError {
	if len(rules) == 0 {
		return nil
	}
	var out []api.ComposeValidationError
	for i, rel := range rules {
		rel = strings.TrimSpace(rel)
		if rel == "" {
			continue
		}
		if !rulePathExists(moduleRoot, projectDir, rel) {
			out = append(out, workflowdiag.EmitDefault(workflowdiag.MustCode("rules_path_missing"),
				fmt.Sprintf("rules[%d]", i),
				map[string]any{"path": rel}))
		}
	}
	return out
}

func rulePathExists(moduleRoot, projectDir, rel string) bool {
	relSlash := filepath.ToSlash(strings.TrimSpace(rel))
	if embedRel, ok := moduleConfigPathToEmbed(relSlash); ok && config.Has(embedRel) {
		return true
	}
	diskRel := filepath.FromSlash(relSlash)
	candidates := []string{filepath.Join(moduleRoot, diskRel)}
	if strings.TrimSpace(projectDir) != "" {
		candidates = append(candidates, filepath.Join(projectDir, settingsoverlay.DirName(), filepath.Base(diskRel)))
		candidates = append(candidates, filepath.Join(projectDir, diskRel))
	}
	for _, p := range candidates {
		if strings.TrimSpace(p) == "" || p == diskRel {
			continue
		}
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}

// moduleConfigPathToEmbed maps a checkout-relative config/… path to an embed Rel.
func moduleConfigPathToEmbed(rel string) (config.Rel, bool) {
	rel = strings.TrimPrefix(rel, "/")
	const prefix = "config/"
	if !strings.HasPrefix(rel, prefix) {
		return "", false
	}
	return config.Rel(strings.TrimPrefix(rel, prefix)), true
}
