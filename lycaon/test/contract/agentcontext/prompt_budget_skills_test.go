package contract

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/skills"
	"github.com/lycaon/lycaon/internal/toolvocab"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func promptBudgetSkillRosterVarsForSet(
	t *testing.T,
	selector skills.Selector,
) map[string]any {
	t.Helper()
	loaded, diagnostics := extpacks.LoadEffectiveSkills(contractcheck.StockCatalog(t))
	if len(diagnostics) != 0 {
		t.Fatalf("stock skill diagnostics: %v", diagnostics)
	}
	if len(loaded) == 0 {
		return nil
	}
	return skillRosterVars(selector.Select(loaded))
}

func skillRosterVars(loaded []skills.Skill) map[string]any {
	surface := prompts.AgentPromptSurface{FullCatalog: true}
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
		vars["full_skill_catalog"] = true
	}
}

func mergePromptBudgetSkillRoster(t *testing.T, vars map[string]any) {
	mergePromptBudgetSkillRosterForSet(
		t, vars, skills.Selector{All: true},
	)
}

func mergeReachableSkillRoster(t *testing.T, vars map[string]any, reader prompts.Reader) {
	t.Helper()
	loaded, diagnostics := extpacks.LoadEffectiveSkills(contractcheck.StockCatalog(t))
	if len(diagnostics) != 0 {
		t.Fatalf("stock skill diagnostics: %v", diagnostics)
	}
	reachable := make([]skills.Skill, 0, len(loaded))
	for _, skill := range loaded {
		if toolvocab.SkillToolsOffered(skill, reader.Held()) {
			reachable = append(reachable, skill)
		}
	}
	roster := skillRosterVars(reachable)
	vars["agent_skills"] = roster["agent_skills"]
	vars["full_skill_catalog"] = roster["full_skill_catalog"]
}

func requirePromptBudgetSkillAvailability(t *testing.T, vars map[string]any, what string) {
	t.Helper()
	list, ok := vars["agent_skills"].([]map[string]any)
	if !ok || len(list) == 0 {
		t.Fatalf("%s measured without bundled skill availability", what)
	}
}

func TestPromptBudgetFixtureIncludesBundledSkillAvailability(t *testing.T) {
	vars := map[string]any{}
	mergePromptBudgetSkillRoster(t, vars)

	if vars["full_skill_catalog"] != true {
		t.Fatal("budget fixture omitted full catalog visibility")
	}
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
