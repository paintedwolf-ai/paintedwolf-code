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

func TestAgentSkillsSectionStatesHowToReadSkills(t *testing.T) {
	prompts.ResetPersonaContractCache()
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	skills := []prompts.AgentSkillView{
		{Name: "alpha-skill", Description: "Alpha procedure."},
		{Name: "zeta-skill", Description: "Zeta procedure."},
	}
	got, err := prompts.RenderPersona(context.Background(), engine, "implementer", map[string]any{
		"agent_skills": skills,
		"visible_tools": []string{
			"read", "write", "edit", "skills_read", "request_tools",
		},
		"visible_tool_summaries": map[string]string{
			"skills_read": "Read a skill's full instructions by name",
		},
	})
	testutil.FailErr(t, "RenderPersona", err)
	idx := strings.Index(got, "### Skills")
	if idx < 0 {
		t.Fatal("expected Skills section")
	}
	section := got[idx:]
	if !strings.Contains(section, "`skills_read({\"need\":\"describe the procedure you need\"})`") {
		t.Fatal("missing actionable skill-read call")
	}
	if !strings.Contains(section, "`skills_read({\"need\":\"selected-skill-name\",\"resource\":\"relative/path\"})`") {
		t.Fatal("missing referenced-file call")
	}
	// The stable prompt teaches the procedure without publishing the catalog.
	if strings.Contains(section, "alpha-skill") || strings.Contains(section, "zeta-skill") {
		t.Fatalf("the standing prompt must not list skills: %q", section[:min(400, len(section))])
	}

	empty, err := prompts.RenderPersona(context.Background(), engine, "implementer", map[string]any{
		"visible_tools": []string{"read", "write", "edit", "skills_read", "request_tools"},
	})
	testutil.FailErr(t, "RenderPersona empty", err)
	if strings.Contains(empty, "### Skills") {
		t.Fatal("empty skills must omit Skills section")
	}
	if strings.Contains(empty, "`skills_read`") {
		t.Fatal("empty skills must omit skills_read from deferred catalog")
	}
}

func TestAgentSkillsIndexProfileGating(t *testing.T) {
	prompts.ResetPersonaContractCache()
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	skills := []prompts.AgentSkillView{
		{Name: "shared-skill", Description: "Shared procedure."},
	}
	with, err := prompts.RenderPersona(context.Background(), engine, "implementer", map[string]any{
		"agent_skills":  skills,
		"visible_tools": []string{"read", "skills_read", "request_tools"},
	})
	testutil.FailErr(t, "implementer", err)
	if !strings.Contains(with, "### Skills") {
		t.Fatal("implementer with skills_read must render the Skills section")
	}

	without, err := prompts.RenderPersona(context.Background(), engine, "path-explorer", map[string]any{
		"agent_skills": skills,
		"visible_tools": []string{
			"read", "grep", "list_dir", "summarize", "render_view", "capture_page", "measure_page",
		},
	})
	testutil.FailErr(t, "path-explorer", err)
	if strings.Contains(without, "### Skills") {
		t.Fatal("profile without skills_read must omit Skills section")
	}
	for _, name := range []string{"verify-visual-change", "mock-before-build"} {
		if strings.Contains(without, name) {
			t.Fatalf("profile without skills_read must not list skill %q", name)
		}
	}
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

func TestSecurityProcedureRetainsAvailableSkillPointer(t *testing.T) {
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	got, err := engine.Render(context.Background(), "units/secure-by-default.md", map[string]any{
		"profile_has_write_tools": true,
		"agent_skills":            listedSkills("use-secrets-without-reading-them"),
	})
	testutil.FailErr(t, "render security procedure", err)
	if !strings.Contains(got, "Read `use-secrets-without-reading-them` first") {
		t.Fatal("available secret-handling procedure was not linked")
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
