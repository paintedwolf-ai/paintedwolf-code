package assembly

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/agentdef"
	"github.com/lycaon/lycaon/internal/catalogview"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/llm/transcript"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestBuildCompletionMessagesOmitsWorkflowBoundaries(t *testing.T) {
	engine := &AssemblyEngine{}
	engine.SetDeps(AssemblyDeps{})
	history := []api.Message{
		{
			ID: "b1", Role: api.MessageRoleSystem, Kind: api.MessageKindWorkflowBoundary,
		},
		{ID: "u1", Role: api.MessageRoleUser, Content: "build a game"},
	}
	out, err := engine.BuildCompletionMessages(context.Background(), &api.Session{ID: "s1"}, history, nil)
	if err != nil {
		t.Fatalf("BuildCompletionMessages: %v", err)
	}
	if len(out) != 2 || out[0].Content != transcript.ContentAuthorityNotice(transcript.AudiencePrivate) || out[1].ID != "u1" {
		t.Fatalf("messages = %+v want user row plus host authority notice", out)
	}
}

func TestMergeWorkerVisibleToolsSetsVisibleTools(t *testing.T) {
	root := kickTestRoot(t)
	pe := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{ModuleRoot: root})
	wantProfile := "explore_readonly"
	engine := &AssemblyEngine{}
	engine.SetDeps(AssemblyDeps{
		Prompts: pe,
		PromptToolLister: func(_ context.Context, _ *api.Session, profileID string) ([]tools.ToolMeta, error) {
			if profileID != wantProfile {
				t.Fatalf("profileID = %q want %q", profileID, wantProfile)
			}
			return []tools.ToolMeta{{Name: "read"}, {Name: "grep"}}, nil
		},
	})
	vars := map[string]any{}
	sess := &api.Session{
		ID:              "child",
		ParentSessionID: "parent",
		AgentType:       "repo-researcher",
		WorkspacePath:   t.TempDir(),
	}
	testPromptSurface(engine).mergeVisibleTools(context.Background(), sess, inject.CoordinatorTurnFrame{}, vars)
	visible, ok := vars["visible_tools"].([]string)
	if !ok || len(visible) != 2 || visible[0] != "read" || visible[1] != "grep" {
		t.Fatalf("visible_tools = %v", vars["visible_tools"])
	}
}

func TestSessionViewAgentProfileDrivesTemplateAndToolSurface(t *testing.T) {
	const agentID = "team-reviewer"
	const profileID = "review_readonly"
	engine := &AssemblyEngine{}
	engine.SetDeps(AssemblyDeps{
		SessionView: func(context.Context, *api.Session) *catalogview.View {
			return &catalogview.View{AgentProfiles: []agentdef.Profile{{
				ID: agentID, ToolProfile: profileID, SystemPromptTemplate: "agents/team-reviewer.md",
			}}}
		},
		PromptToolLister: func(_ context.Context, _ *api.Session, got string) ([]tools.ToolMeta, error) {
			if got != profileID {
				t.Fatalf("profileID = %q want %q", got, profileID)
			}
			return []tools.ToolMeta{{Name: "read"}}, nil
		},
	})
	sess := &api.Session{ID: "child", ParentSessionID: "parent", AgentType: agentID}
	ref, err := testPromptSurface(engine).resolveSystemPromptRef(context.Background(), sess)
	if err != nil {
		t.Fatalf("resolveSystemPromptRef: %v", err)
	}
	if ref != "agents/team-reviewer.md" {
		t.Fatalf("template ref = %q", ref)
	}
	vars := map[string]any{}
	testPromptSurface(engine).mergeVisibleTools(context.Background(), sess, inject.CoordinatorTurnFrame{}, vars)
	if got, ok := vars["visible_tools"].([]string); !ok || len(got) != 1 || got[0] != "read" {
		t.Fatalf("visible_tools = %v", vars["visible_tools"])
	}
}

func TestMergeEffectivePromptSurfacePrefersMachine(t *testing.T) {
	called := false
	engine := &AssemblyEngine{}
	engine.SetDeps(AssemblyDeps{
		EffectivePromptSurface: func(context.Context, *api.Session) prompts.AgentPromptSurface {
			called = true
			return prompts.AgentPromptSurface{Fingerprint: "callback"}
		},
	})
	vars := map[string]any{}
	testPromptSurface(engine).mergeEffectivePromptSurfaceWithTurn(context.Background(), &api.Session{ID: "s1"}, inject.CoordinatorTurnFrame{
		Machine: inject.Machine{
			ProfileID: "coordinator",
			Surface:   prompts.AgentPromptSurface{Fingerprint: "machine"},
		},
	}, nil, vars)
	if called {
		t.Fatal("compiled machine must not re-enter EffectivePromptSurface")
	}
	if got, _ := vars["prompt_surface_fingerprint"].(string); got != "machine" {
		t.Fatalf("fingerprint = %q", got)
	}
}

func TestMergeWorkerVisibleToolsHidesSkillsReadFromMachine(t *testing.T) {
	root := kickTestRoot(t)
	engine := &AssemblyEngine{}
	engine.SetDeps(AssemblyDeps{
		Prompts: prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{ModuleRoot: root}),
		PromptToolLister: func(context.Context, *api.Session, string) ([]tools.ToolMeta, error) {
			return []tools.ToolMeta{{Name: "read"}, {Name: "skills_read"}}, nil
		},
	})
	sess := &api.Session{ID: "child", ParentSessionID: "parent", AgentType: "repo-researcher"}
	vars := map[string]any{}
	testPromptSurface(engine).mergeVisibleTools(context.Background(), sess, inject.CoordinatorTurnFrame{
		Machine: inject.Machine{ProfileID: "implement", SkillCount: 0},
	}, vars)
	got, _ := vars["visible_tools"].([]string)
	if len(got) != 1 || got[0] != "read" {
		t.Fatalf("visible_tools = %v", got)
	}
}

func TestCompiledMachineRefreshesCatalogVisibility(t *testing.T) {
	selection := &selectionFixture{}
	engine := &AssemblyEngine{}
	engine.SetDeps(AssemblyDeps{Selection: selection})
	frame := inject.CoordinatorTurnFrame{Machine: inject.Machine{ProfileID: "coordinator", Surface: prompts.AgentPromptSurface{Skills: []prompts.AgentSkillView{{Name: "review", Description: "Review the change."}}}}}
	sess := &api.Session{ID: "s"}
	vars := map[string]any{}
	projection := testPromptSurface(engine)
	projection.mergeEffectivePromptSurfaceWithTurn(t.Context(), sess, frame, nil, vars)
	first := vars["prompt_surface_fingerprint"]
	selection.full = true
	projection.mergeEffectivePromptSurfaceWithTurn(t.Context(), sess, frame, nil, vars)
	if vars["full_skill_catalog"] != true || vars["prompt_surface_fingerprint"] == first {
		t.Fatal("mid-turn failure did not refresh catalog projection")
	}
	frame.Machine.Surface.Skills[0].Description = "Review accessibility."
	first = vars["prompt_surface_fingerprint"]
	projection.mergeEffectivePromptSurfaceWithTurn(t.Context(), sess, frame, nil, vars)
	if vars["prompt_surface_fingerprint"] == first {
		t.Fatal("changed catalog reused prompt identity")
	}
}

type selectionFixture struct {
	loaded map[string]bool
	full   bool
}

func (s *selectionFixture) LoadedTools(string) map[string]bool { return s.loaded }
func (s *selectionFixture) FullCatalog(string) bool            { return s.full }

func TestStandingPromptFiltersSkillsByTurnSurfaceReader(t *testing.T) {
	const promoteSurface = "implement_overlay_promote"
	root := kickTestRoot(t)
	pe := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{ModuleRoot: root})
	profiles, err := sandbox.LoadToolProfiles()
	if err != nil {
		t.Fatalf("LoadToolProfiles: %v", err)
	}
	plan, err := surface.CompileToolPlan(surface.TurnProfile{SurfaceID: promoteSurface}, 1)
	if err != nil {
		t.Fatalf("CompileToolPlan(%s): %v", promoteSurface, err)
	}
	promoteReader, err := prompts.NewReader(prompts.ReaderInput{
		ProfileID: prompts.CoordinatorProfileID,
		SurfaceID: promoteSurface,
		Profiles:  profiles,
		Plan:      plan,
		RootCount: 1,
		WebSearch: true,
	})
	if err != nil {
		t.Fatalf("NewReader(%s): %v", promoteSurface, err)
	}
	vars := map[string]any{
		"full_skill_catalog": true,
		"agent_skills": []map[string]any{
			{
				"name":        "apply-a-structural-codemod",
				"description": "Rewrite one repeated code shape across many files with a structural pattern and `code_rewrite`, previewed before it is applied.",
			},
			{
				"name":        "custom-safe-skill",
				"description": "Inspect the board and coordinate remaining scope.",
			},
		},
	}
	rendered, err := renderStandingPrompt(t.Context(), pe, standingPromptInputs{
		Profile: standingProfileInputs{SurfaceID: promoteSurface},
		Vars:    vars,
	}.withReader(promoteReader))
	if err != nil {
		t.Fatalf("renderStandingPrompt: %v", err)
	}
	if strings.Contains(rendered, "apply-a-structural-codemod") || strings.Contains(rendered, "code_rewrite") {
		t.Fatalf("standing prompt on %s leaked unoffered skill/tool:\n%s", promoteSurface, rendered)
	}
	if !strings.Contains(rendered, "custom-safe-skill") {
		t.Fatalf("standing prompt on %s dropped offered skill:\n%s", promoteSurface, rendered)
	}

	viewOcc := map[string]any{
		"agent_skills": []prompts.AgentSkillView{
			{Name: "apply-a-structural-codemod", Description: "Rewrite with `code_rewrite`."},
			{Name: "", Description: "Blank."},
		},
	}
	filterOccurrenceSkillsForReader(pe, promoteReader, viewOcc)
	if _, stillPresent := viewOcc["agent_skills"]; stillPresent {
		t.Fatalf("expected agent_skills cleared when all AgentSkillView entries require unoffered tools: %v", viewOcc)
	}

	mapOcc := map[string]any{
		"agent_skills": []map[string]any{{"name": "apply-a-structural-codemod", "description": "Rewrite with `code_rewrite`."}},
	}
	filterOccurrenceSkillsForReader(pe, promoteReader, mapOcc)
	if _, stillPresent := mapOcc["agent_skills"]; stillPresent {
		t.Fatalf("expected agent_skills cleared when all map entries require unoffered tools: %v", mapOcc)
	}
	filterOccurrenceSkillsForReader(pe, prompts.Reader{}, mapOcc)
}
