package contract

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/skills"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func promptBudgetSkillRosterVarsForSet(
	t *testing.T,
	selector skills.Selector,
) map[string]any {
	t.Helper()
	loaded, _ := extpacks.LoadEffectiveSkills(extpacks.Active())
	if len(loaded) == 0 {
		return nil
	}
	loaded = selector.Select(loaded)
	// Include every available skill so budget fixtures exercise skill availability.
	surface := prompts.AgentPromptSurface{}
	for _, sk := range loaded {
		surface.Skills = append(surface.Skills, prompts.AgentSkillView{
			Name:        sk.Name,
			Description: strings.TrimSpace(sk.Description),
		})
	}
	return prompts.AgentPromptSurfaceTemplateVars(surface)
}

func mergePromptBudgetSkillRosterForSet(
	t *testing.T,
	vars map[string]any,
	selector skills.Selector,
) {
	t.Helper()
	if vars == nil {
		return
	}
	roster := promptBudgetSkillRosterVarsForSet(t, selector)
	if list, ok := roster["agent_skills"].([]map[string]any); ok && len(list) > 0 {
		vars["agent_skills"] = list
	}
}

func mergePromptBudgetSkillRoster(t *testing.T, vars map[string]any) {
	mergePromptBudgetSkillRosterForSet(
		t, vars, skills.Selector{All: true},
	)
}

func requirePromptBudgetSkillAvailability(t *testing.T, vars map[string]any, what string) {
	t.Helper()
	list, ok := vars["agent_skills"].([]map[string]any)
	if !ok || len(list) == 0 {
		t.Fatalf("%s measured without bundled skill availability", what)
	}
}

func TestPromptBudgetFixtureIncludesBundledSkillAvailability(t *testing.T) {
	contractcheck.ActivateStockCatalog(t)
	vars := map[string]any{}
	mergePromptBudgetSkillRoster(t, vars)

	list, ok := vars["agent_skills"].([]map[string]any)
	if !ok || len(list) == 0 {
		t.Fatalf("budget fixtures must carry bundled skill availability, got %#v", vars["agent_skills"])
	}
	// A floor, not the catalog size, so the fixture tracks bundled skills.
	if len(list) < 20 {
		t.Fatalf("bundled skill fixture shrank to %d entries", len(list))
	}
	described := 0
	nameOnly := 0
	for _, row := range list {
		if name, _ := row["name"].(string); name == "" {
			t.Fatalf("roster entry without a name: %#v", row)
		}
		if desc, _ := row["description"].(string); strings.TrimSpace(desc) == "" {
			nameOnly++
		} else {
			described++
		}
	}
	if described != len(list) || nameOnly != 0 {
		t.Fatalf("every available skill must have a description: described=%d name_only=%d", described, nameOnly)
	}
}
