package contract

import (
	"bytes"
	"fmt"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/test/contract/internal/sizebudget"
	"gopkg.in/yaml.v3"
)

const promptBudgetPolicyPath = "lycaon/config/packs/painted-wolf/platform/host/prompt-budgets.yaml"

var promptBudgetSuite = sizebudget.Suite{
	Name:       "prompts",
	PolicyPath: promptBudgetPolicyPath,
	Refresh:    "UPDATE_PROMPT_BUDGETS=1 ./task budgets",
	Categories: map[string]sizebudget.Category{
		"worker_personas": {
			Unit:     "bytes",
			Measures: "Rendered worker persona stack (archetype, delta, playbooks, and host worker context) per agent in _persona-contract.yaml",
			Remedy:   "Keep worker prompts focused; do not repeat coordinator policy in personas or playbooks.",
		},
		"coordinator_tripartite": {
			Unit:     "bytes",
			Measures: "Full coordinator system prompt (core, mode partial, and posture surface) for one ContextDietMatrix fixture",
			Remedy:   "Shared partials reach many fixtures; shorten them before mode-specific copy.",
		},
		"coordinator_injects": {
			Unit:     "bytes",
			Measures: "Ephemeral inject block appended per turn, rendered from its PromptBudgetInjectMatrix fixture",
			Remedy:   "Keep per-turn state concise and host-authoritative; move procedure into guidance read on demand.",
		},
		"agent_templates": {
			Unit:     "bytes",
			Measures: "Non-worker agent system template before tripartite assembly",
			Remedy:   "Move situational guidance into units or kicks that render only when they apply.",
		},
		"kicks": {
			Unit:     "bytes",
			Measures: "Host kick template, including parsed partials and phase obligations",
			Remedy:   "A kick answers one situation; point to procedure instead of restating it.",
		},
		"tool_surfaces": {
			Unit:     "bytes",
			Measures: "Wire-facing tool definitions a profile puts on every request (deferred tools excluded)",
			Remedy:   "Change cold tools from sticky to true so their schemas load on demand through request_tools.",
		},
	},
}

// promptSizePolicy views the shipped prompt size limits as a budget policy.
func promptSizePolicy(sizes prompts.PromptSizes) sizebudget.Policy {
	policy := sizebudget.Policy{
		Limits:        map[string]sizebudget.Limit{},
		Grandfathered: sizes.Grandfathered,
		Exceptions:    map[string]map[string]sizebudget.Exception{},
	}
	for name, limit := range sizes.Limits {
		policy.Limits[name] = sizebudget.Limit{Warn: limit.Warn, Limit: limit.Limit}
	}
	for name, entries := range sizes.Exceptions {
		policy.Exceptions[name] = map[string]sizebudget.Exception{}
		for id, exception := range entries {
			policy.Exceptions[name][id] = sizebudget.Exception{Cap: exception.Cap, Reason: exception.Reason}
		}
	}
	return policy
}

// decodePromptSizePolicy reads the size policy from a prompt-budgets.yaml body.
func decodePromptSizePolicy(raw []byte) (sizebudget.Policy, error) {
	var node yaml.Node
	if err := yaml.Unmarshal(raw, &node); err != nil {
		return sizebudget.Policy{}, fmt.Errorf("decode prompt budgets: %w", err)
	}
	if len(node.Content) == 0 {
		return sizebudget.Policy{}, fmt.Errorf("prompt budgets are empty")
	}
	sizes := mappingValue(node.Content[0], "sizes")
	if sizes == nil {
		return sizebudget.Policy{}, fmt.Errorf("prompt budgets: sizes missing")
	}
	if err := sizebudget.RequireIntegers(sizes); err != nil {
		return sizebudget.Policy{}, fmt.Errorf("prompt budgets: %w", err)
	}
	var document struct {
		Sizes prompts.PromptSizes `yaml:"sizes"`
	}
	if err := yaml.Unmarshal(raw, &document); err != nil {
		return sizebudget.Policy{}, fmt.Errorf("decode prompt budgets: %w", err)
	}
	policy := promptSizePolicy(document.Sizes)
	if err := policy.Validate(promptBudgetSuite.CategoryNames()); err != nil {
		return sizebudget.Policy{}, fmt.Errorf("prompt budgets: %w", err)
	}
	return policy, nil
}

// promptBudgetSources lists the files each measured prompt is made from.
func promptBudgetSources(catalog map[string]map[string]promptBudgetEntry) map[string]map[string][]string {
	out := map[string]map[string][]string{}
	for name, entries := range catalog {
		out[name] = map[string][]string{}
		for id, entry := range entries {
			out[name][id] = entry.TrimPaths
		}
	}
	return out
}

// promptBudgetDetail explains what a failing prompt measures and where to trim.
func promptBudgetDetail(catalog map[string]map[string]promptBudgetEntry) func(sizebudget.Finding) []string {
	return func(f sizebudget.Finding) []string {
		entry, ok := catalog[f.Category][f.ID]
		if !ok {
			return nil
		}
		var lines []string
		if entry.Fixture != "" {
			lines = append(lines, "fixture: "+entry.Fixture)
		}
		if len(entry.TrimPaths) > 0 {
			lines = append(lines, "trim (edit these first):")
			for _, path := range entry.TrimPaths {
				lines = append(lines, "  - "+path)
			}
		}
		if entry.Advice != "" {
			lines = append(lines, "advice: "+entry.Advice)
		}
		return lines
	}
}

// tightenPromptBudgetFile rewrites only sizes.grandfathered, so the comments
// and runtime limits around it keep their authored form.
func tightenPromptBudgetFile(raw []byte, grandfathered map[string]map[string]int) ([]byte, error) {
	var document yaml.Node
	if err := yaml.Unmarshal(raw, &document); err != nil {
		return nil, fmt.Errorf("decode prompt budgets: %w", err)
	}
	sizes := mappingValue(document.Content[0], "sizes")
	if sizes == nil {
		return nil, fmt.Errorf("prompt budgets: sizes missing")
	}
	target := mappingValue(sizes, "grandfathered")
	if target == nil {
		return nil, fmt.Errorf("prompt budgets: sizes.grandfathered missing")
	}
	var replacement yaml.Node
	if err := replacement.Encode(grandfathered); err != nil {
		return nil, fmt.Errorf("encode grandfathered prompt caps: %w", err)
	}
	target.Kind, target.Style, target.Tag, target.Content = replacement.Kind, replacement.Style, replacement.Tag, replacement.Content
	var out bytes.Buffer
	encoder := yaml.NewEncoder(&out)
	encoder.SetIndent(4)
	if err := encoder.Encode(&document); err != nil {
		return nil, fmt.Errorf("encode prompt budgets: %w", err)
	}
	if err := encoder.Close(); err != nil {
		return nil, fmt.Errorf("encode prompt budgets: %w", err)
	}
	return out.Bytes(), nil
}

func mappingValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}

func promptBudgetPolicyFile(root string) string {
	return filepath.Join(root, filepath.FromSlash(promptBudgetPolicyPath))
}
