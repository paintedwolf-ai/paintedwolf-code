package assembly

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/agentdef"
	"github.com/lycaon/lycaon/internal/catalogview"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/llm/transcript"
	"github.com/lycaon/lycaon/internal/prompts"
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
	if len(out) != 2 || out[0].Content != transcript.ContentAuthorityNotice() || out[1].ID != "u1" {
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
	engine.mergeVisibleTools(context.Background(), sess, inject.CoordinatorTurnFrame{}, vars)
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
	ref, err := engine.resolveSystemPromptRef(context.Background(), sess)
	if err != nil {
		t.Fatalf("resolveSystemPromptRef: %v", err)
	}
	if ref != "agents/team-reviewer.md" {
		t.Fatalf("template ref = %q", ref)
	}
	vars := map[string]any{}
	engine.mergeVisibleTools(context.Background(), sess, inject.CoordinatorTurnFrame{}, vars)
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
	engine.mergeEffectivePromptSurface(context.Background(), &api.Session{ID: "s1"}, inject.CoordinatorTurnFrame{
		Machine: inject.Machine{
			ProfileID: "coordinator",
			Surface:   prompts.AgentPromptSurface{Fingerprint: "machine"},
		},
	}, vars)
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
	engine.mergeVisibleTools(context.Background(), sess, inject.CoordinatorTurnFrame{
		Machine: inject.Machine{ProfileID: "implement", SkillCount: 0},
	}, vars)
	got, _ := vars["visible_tools"].([]string)
	if len(got) != 1 || got[0] != "read" {
		t.Fatalf("visible_tools = %v", got)
	}
}
