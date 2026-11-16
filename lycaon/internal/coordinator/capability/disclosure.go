package capability

import (
	"fmt"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/toolscope"
)

// DisclosurePolicy controls whether an unavailable entry is named.
type DisclosurePolicy string

const (
	DisclosureShow DisclosurePolicy = "show"
	DisclosureHide DisclosurePolicy = "hide"
)

// DisclosureRegistry holds defaults, overrides, and exemptions.
type DisclosureRegistry struct {
	DefaultAgents  DisclosurePolicy
	DefaultTools   DisclosurePolicy
	AgentOverrides map[string]DisclosurePolicy
	ToolOverrides  map[string]DisclosurePolicy
	AgentExempt    map[string]string
	ToolExempt     map[string]string
}

type disclosurePolicyFile struct {
	Defaults struct {
		Agents string `yaml:"agents"`
		Tools  string `yaml:"tools"`
	} `yaml:"defaults"`
	Overrides struct {
		Agents map[string]string `yaml:"agents"`
		Tools  map[string]string `yaml:"tools"`
	} `yaml:"overrides"`
	Exempt struct {
		Agents map[string]string `yaml:"agents"`
		Tools  map[string]string `yaml:"tools"`
	} `yaml:"exempt"`
}

// LoadDisclosureRegistry loads the bundled disclosure policy.
func LoadDisclosureRegistry() (DisclosureRegistry, error) {
	data, err := config.Read(config.DisclosurePolicy)
	if err != nil {
		return DisclosureRegistry{}, fmt.Errorf("read disclosure policy: %w", err)
	}
	var raw disclosurePolicyFile
	if err := config.DecodeYAML(data, &raw); err != nil {
		return DisclosureRegistry{}, fmt.Errorf("parse disclosure policy: %w", err)
	}
	defaultAgents, err := parseDisclosurePolicy(raw.Defaults.Agents)
	if err != nil {
		return DisclosureRegistry{}, fmt.Errorf("default agents: %w", err)
	}
	defaultTools, err := parseDisclosurePolicy(raw.Defaults.Tools)
	if err != nil {
		return DisclosureRegistry{}, fmt.Errorf("default tools: %w", err)
	}
	reg := DisclosureRegistry{
		DefaultAgents:  defaultAgents,
		DefaultTools:   defaultTools,
		AgentOverrides: map[string]DisclosurePolicy{},
		ToolOverrides:  map[string]DisclosurePolicy{},
		AgentExempt:    copyStringMap(raw.Exempt.Agents),
		ToolExempt:     copyStringMap(raw.Exempt.Tools),
	}
	for id, policy := range raw.Overrides.Agents {
		id = strings.TrimSpace(id)
		if id == "" {
			return DisclosureRegistry{}, fmt.Errorf("agent override has an empty id")
		}
		parsed, err := parseDisclosurePolicy(policy)
		if err != nil {
			return DisclosureRegistry{}, fmt.Errorf("agent %q: %w", id, err)
		}
		reg.AgentOverrides[id] = parsed
	}
	for name, policy := range raw.Overrides.Tools {
		name = strings.TrimSpace(name)
		if name == "" {
			return DisclosureRegistry{}, fmt.Errorf("tool override has an empty name")
		}
		parsed, err := parseDisclosurePolicy(policy)
		if err != nil {
			return DisclosureRegistry{}, fmt.Errorf("tool %q: %w", name, err)
		}
		reg.ToolOverrides[name] = parsed
	}
	return reg, nil
}

func parseDisclosurePolicy(raw string) (DisclosurePolicy, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "show":
		return DisclosureShow, nil
	case "hide":
		return DisclosureHide, nil
	default:
		return "", fmt.Errorf("policy %q must be show or hide", raw)
	}
}

func copyStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	return out
}

// AgentDisclosurePolicy resolves the policy for an agent.
func AgentDisclosurePolicy(registry DisclosureRegistry, agentID string) DisclosurePolicy {
	agentID = strings.TrimSpace(agentID)
	if _, ok := registry.AgentExempt[agentID]; ok {
		return ""
	}
	if p, ok := registry.AgentOverrides[agentID]; ok {
		return p
	}
	return registry.DefaultAgents
}

// ToolDisclosurePolicy resolves the policy for a gateable coordinator tool.
func ToolDisclosurePolicy(registry DisclosureRegistry, toolName string) DisclosurePolicy {
	toolName = strings.TrimSpace(toolName)
	if _, ok := registry.ToolExempt[toolName]; ok {
		return ""
	}
	if p, ok := registry.ToolOverrides[toolName]; ok {
		return p
	}
	return registry.DefaultTools
}

// FilterAgentsForDisclosure keeps disallowed agents marked for disclosure.
func FilterAgentsForDisclosure(agents []prompts.SpawnAgentView, registry DisclosureRegistry) []prompts.SpawnAgentView {
	if len(agents) == 0 {
		return nil
	}
	out := make([]prompts.SpawnAgentView, 0, len(agents))
	for _, a := range agents {
		if AgentDisclosurePolicy(registry, a.ID) != DisclosureShow {
			continue
		}
		out = append(out, a)
	}
	return out
}

// GateableCoordinatorTools returns every tool name that can appear on a coordinator surface.
func GateableCoordinatorTools() ([]string, error) {
	plans, err := surface.CompileToolPlans(1)
	if err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	for _, plan := range plans {
		for _, name := range plan.AddressableNames() {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			seen[name] = struct{}{}
		}
	}
	allow, err := toolscope.NoFolderAllowlist()
	if err != nil {
		return nil, err
	}
	for _, name := range allow {
		name = strings.TrimSpace(name)
		if name != "" {
			seen[name] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out, nil
}
