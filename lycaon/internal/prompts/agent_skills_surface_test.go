package prompts_test

import (
	"context"
	"github.com/lycaon/lycaon/internal/configlayout"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testutil"
)

func listedSkills(names ...string) []prompts.AgentSkillView {
	out := make([]prompts.AgentSkillView, 0, len(names))
	for _, name := range names {
		out = append(out, prompts.AgentSkillView{Name: name})
	}
	return out
}

func TestNamedSkillReferencesRequireExactRosterMembership(t *testing.T) {
	root := configlayout.FindModuleRoot()
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{ModuleRoot: root})

	tests := []struct {
		ref       string
		vars      map[string]any
		name      string
		available string
	}{
		{
			ref:  "partials/coordinator-mode-shell-investigate.md",
			vars: map[string]any{"agent_skills": listedSkills("unrelated-skill")},
			name: "ask-for-a-decision",
		},
		{
			ref: "partials/coordinator-ask-user-discipline.md",
			vars: map[string]any{
				"profile_has_ask_user": true,
				"agent_skills":         listedSkills("unrelated-skill"),
			},
			name: "ask-for-a-decision",
		},
		{
			ref:  "partials/external-facts-verify.md",
			vars: map[string]any{"agent_skills": listedSkills("unrelated-skill")},
			name: "research-current-information",
		},
		{
			ref: "units/secure-by-default.md",
			vars: map[string]any{
				"profile_has_write_tools": true,
				"agent_skills":            listedSkills("unrelated-skill"),
			},
			name:      "use-secrets-without-reading-them",
			available: "verify the service rejects missing and incorrect credentials",
		},
		{
			ref: "units/visual-show-the-design.md",
			vars: map[string]any{
				"agent_skills":          listedSkills("verify-visual-change"),
				"visual_show_available": true,
				"visual_show_page":      true,
				"visual_show_terminal":  true,
			},
			name:      "verify-terminal-change",
			available: "verify-visual-change",
		},
	}
	for _, tt := range tests {
		got, err := engine.Render(context.Background(), tt.ref, tt.vars)
		testutil.FailErr(t, "render "+tt.ref, err)
		if strings.Contains(got, tt.name) {
			t.Fatalf("%s references unavailable skill %q:\n%s", tt.ref, tt.name, got)
		}
		if tt.available != "" && !strings.Contains(got, tt.available) {
			t.Fatalf("%s omitted available skill %q:\n%s", tt.ref, tt.available, got)
		}
	}
}

func TestNamedSkillMembershipCannotBeSpoofedByTemplateVars(t *testing.T) {
	root := configlayout.FindModuleRoot()
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{ModuleRoot: root})
	got, err := engine.Render(context.Background(), "partials/external-facts-verify.md", map[string]any{
		"agent_skills": listedSkills("unrelated-skill"),
		"agent_has_skill_research_current_information": true,
	})
	testutil.FailErr(t, "render external facts", err)
	if strings.Contains(got, "research-current-information") {
		t.Fatalf("caller-supplied membership flag bypassed roster derivation:\n%s", got)
	}
}

func TestNamedSkillMembershipDoesNotFoldCollidingNames(t *testing.T) {
	root := configlayout.FindModuleRoot()
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{ModuleRoot: root})
	for _, collision := range []string{
		"Research-current-information",
		"research current information",
		"research_current_information",
		"research-current-information!",
	} {
		got, err := engine.Render(context.Background(), "partials/external-facts-verify.md", map[string]any{
			"agent_skills": listedSkills(collision),
		})
		testutil.FailErr(t, "render external facts", err)
		if strings.Contains(got, "research-current-information") {
			t.Fatalf("colliding roster name %q enabled another skill reference:\n%s", collision, got)
		}
	}
}

func TestFullSkillCatalogListsNamesAndDescriptions(t *testing.T) {
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{ModuleRoot: configlayout.FindModuleRoot()})
	surface := prompts.AgentPromptSurface{FullCatalog: true, Skills: []prompts.AgentSkillView{{Name: "review-accessibility", Description: "Review keyboard navigation."}, {Name: "verify-change", Description: "Run relevant verification."}}}
	got, err := engine.Render(context.Background(), "partials/agent-skills.md", prompts.AgentPromptSurfaceTemplateVars(surface))
	testutil.FailErr(t, "render full skill catalog", err)
	for _, skill := range surface.Skills {
		if !strings.Contains(got, skill.Name) || !strings.Contains(got, skill.Description) {
			t.Fatalf("missing skill card %+v: %s", skill, got)
		}
	}
	surface.FullCatalog = false
	selected, err := engine.Render(context.Background(), "partials/agent-skills.md", prompts.AgentPromptSurfaceTemplateVars(surface))
	testutil.FailErr(t, "render selected skill availability", err)
	if strings.Contains(selected, "review-accessibility") {
		t.Fatal("healthy selection rendered the full catalog")
	}
}
