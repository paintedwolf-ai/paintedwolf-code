package contract

import (
	"context"
	"sort"

	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/skills"
)

// PromptBudgetRegistry lists every artifact measured by TestRenderedPromptsWithinBudget.
type PromptBudgetRegistry struct {
	LycaonRoot     string
	WorkerPersonas map[string]prompts.AgentPersonaDef
	AgentSkills    map[string]skills.Selector
	Tripartite     []ContextDietMatrixRow
	Injects        []PromptBudgetInjectSpec
	KickIDs        []string
	AgentTemplates map[string]string // profile id → system_prompt_template ref
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
	return &PromptBudgetRegistry{
		LycaonRoot:     lycaonRoot,
		WorkerPersonas: persona.Agents,
		AgentSkills:    agentSkills,
		Tripartite:     ContextDietMatrix,
		Injects:        PromptBudgetInjectMatrix,
		KickIDs:        kickIDs,
		AgentTemplates: agentTemplates,
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
