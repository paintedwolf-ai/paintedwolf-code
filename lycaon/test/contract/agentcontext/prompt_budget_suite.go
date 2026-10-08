package contract

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"

	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/promptunit"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/skills"
	"github.com/lycaon/lycaon/test/contract/internal/sizebudget"
	"gopkg.in/yaml.v3"
)

const promptBudgetPolicyPath = "lycaon/config/packs/painted-wolf/platform/host/prompt-budgets.yaml"

var promptBudgetSuite = sizebudget.Suite{
	Name:       "prompts",
	PolicyPath: promptBudgetPolicyPath,
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
		"units": {
			Unit:     "bytes",
			Measures: "One instruction unit rendered on its own; it rides a turn only when its tools are offered and the decision engine keeps it",
			Remedy:   "A unit carries one procedure or rule for the tools it attaches to; split a unit that serves two requests.",
		},
		"coordinator_tool_surfaces": {
			Unit:     "bytes",
			Measures: "Wire-facing tool definitions one coordinator surface puts on every request: its plan's immediate tools, trimmed for the coordinator",
			Remedy:   "Move cold tools from the surface floor to its loadable list so their schemas load on demand through request_tools.",
		},
		"worker_tool_surfaces": {
			Unit:     "bytes",
			Measures: "Wire-facing tool definitions one worker tool profile puts on every request: its sticky tools",
			Remedy:   "Change cold tools from sticky to true in the profile so their schemas load on demand through request_tools.",
		},
	},
}

// promptSizePolicy views the shipped prompt size limits as a budget policy.
func promptSizePolicy(sizes prompts.PromptSizes) sizebudget.Policy {
	policy := sizebudget.Policy{
		Limits:     map[string]sizebudget.Limit{},
		Exceptions: map[string]map[string]sizebudget.Exception{},
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

// PromptBudgetRegistry lists every artifact measured by TestRenderedPromptsWithinBudget.
type PromptBudgetRegistry struct {
	LycaonRoot     string
	WorkerPersonas map[string]prompts.AgentPersonaDef
	AgentSkills    map[string]skills.Selector
	Tripartite     []ContextDietMatrixRow
	Injects        []PromptBudgetInjectSpec
	KickIDs        []string
	AgentTemplates map[string]string // profile id → system_prompt_template ref
	// Units are the stock instruction units a turn carries only when selected.
	Units []promptunit.Unit
	// CoordinatorSurfaces lists every coordinator surface a plan compiles for.
	CoordinatorSurfaces []string
}

// LoadPromptBudgetRegistry discovers budget artifacts from production config.
func LoadPromptBudgetRegistry(lycaonRoot string) (*PromptBudgetRegistry, error) {
	persona, err := prompts.LoadPersonaContract()
	if err != nil {
		return nil, err
	}
	kickIDs, err := kickTemplateIDs()
	if err != nil {
		return nil, err
	}
	reg := orchestration.NewMemoryAgentRegistry()
	if err := orchestration.LoadRequiredAgentRegistry(context.Background(), reg); err != nil {
		return nil, err
	}
	agentTemplates := map[string]string{}
	agentSkills := map[string]skills.Selector{}
	for _, profile := range reg.List() {
		agentSkills[profile.ID] = profile.Skills
		if persona.IsWorkerAgent(profile.ID) {
			continue
		}
		ref := profile.SystemPromptTemplate
		if ref == "" {
			continue
		}
		agentTemplates[profile.ID] = ref
	}
	units, err := prompts.UnitCatalogFor(nil)
	if err != nil {
		return nil, err
	}
	var stockUnits []promptunit.Unit
	for _, unit := range units.Units() {
		if unit.Stock {
			stockUnits = append(stockUnits, unit)
		}
	}
	plans, err := surface.CompileToolPlans(1)
	if err != nil {
		return nil, err
	}
	coordinatorSurfaces := make([]string, 0, len(plans))
	for id := range plans {
		coordinatorSurfaces = append(coordinatorSurfaces, id)
	}
	sort.Strings(coordinatorSurfaces)
	return &PromptBudgetRegistry{
		LycaonRoot:          lycaonRoot,
		WorkerPersonas:      persona.Agents,
		AgentSkills:         agentSkills,
		Tripartite:          ContextDietMatrix,
		Injects:             PromptBudgetInjectMatrix,
		KickIDs:             kickIDs,
		AgentTemplates:      agentTemplates,
		Units:               stockUnits,
		CoordinatorSurfaces: coordinatorSurfaces,
	}, nil
}

// ToolProfileIDs lists production tool profile ids for tool-surface budgets.
func (r *PromptBudgetRegistry) ToolProfileIDs() []string {
	if r == nil {
		return nil
	}
	profiles, err := sandbox.LoadToolProfiles()
	if err != nil {
		return nil
	}
	ids := make([]string, 0, len(profiles))
	for _, p := range profiles {
		ids = append(ids, p.ID)
	}
	sort.Strings(ids)
	return ids
}

func (r *PromptBudgetRegistry) WorkerPersonaIDs() []string {
	if r == nil {
		return nil
	}
	ids := make([]string, 0, len(r.WorkerPersonas))
	for id := range r.WorkerPersonas {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
