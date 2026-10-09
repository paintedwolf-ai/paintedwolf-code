package assembly

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestRenderSystemPromptUnknownAgentFallsBack(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	engine := &AssemblyEngine{}
	engine.SetDeps(AssemblyDeps{
		Prompts: prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{ModuleRoot: root}),
	})
	sess := &api.Session{AgentType: "nonexistent-agent-xyz", WorkspacePath: t.TempDir()}
	pe, _, err := testPromptSurface(engine).projectPromptsSnapshot(context.Background(), sess)
	testutil.FailErr(t, "engine.projectPromptsSnapshot failed", err)
	out, err := testPromptSurface(engine).renderSystemPromptWithEngine(context.Background(), pe, sess,
		"agents/coordinator-core.md", map[string]any{"project_dir": t.TempDir()})
	testutil.FailErr(t, "engine.renderSystemPromptWithEngine failed", err)
	if strings.TrimSpace(out) == "" {
		t.Fatal("expected fallback template render")
	}
}

func TestReviewEvidenceUnitsUseCapabilitiesAndRespectOmissions(t *testing.T) {
	for _, omitted := range []bool{false, true} {
		name := "retained"
		if omitted {
			name = "omitted"
		}
		t.Run(name, func(t *testing.T) {
			pe := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{ModuleRoot: kickTestRoot(t)})
			engine := &AssemblyEngine{}
			engine.SetDeps(AssemblyDeps{
				Prompts: pe,
				OmittedUnits: func(string) map[string]bool {
					return map[string]bool{"claim-evidence": omitted, "native-recall-tool": true}
				},
			})
			sess := &api.Session{ID: "review", AgentType: "coordinator", Posture: api.SessionPostureVet, WorkspacePath: t.TempDir()}
			frame := inject.CoordinatorTurnFrame{RunContext: api.CoordinatorRunContext{
				PhaseCoordinatorSurface: "review_adjudicate",
				AllowedAgents:           []string{"skeptic", "web-researcher"},
			}}
			out, profile, _, err := testPromptSurface(engine).renderTripartiteCoordinatorPrompt(t.Context(), pe, sess, frame, nil, "", nil,
				map[string]any{"root_count": 1, "project_dir": sess.WorkspacePath})
			testutil.FailErr(t, "render review evidence units", err)
			if profile.SurfaceID != "review_adjudicate" {
				t.Fatalf("surface = %q, want review_adjudicate", profile.SurfaceID)
			}
			for _, row := range []string{"| How code behaves |", "| Components connect or feed |"} {
				if strings.Contains(out, row) == omitted {
					t.Fatalf("row %q present=%v with omitted=%v", row, strings.Contains(out, row), omitted)
				}
			}
			if strings.Contains(out, "Known handle:") {
				t.Fatal("omitted recall unit rendered")
			}
		})
	}
}

func TestCoordinatorUnitsKeepLoadedToolsAndWebGate(t *testing.T) {
	pe := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{ModuleRoot: kickTestRoot(t)})
	for _, webEnabled := range []bool{false, true} {
		engine := &AssemblyEngine{}
		engine.SetDeps(AssemblyDeps{
			Prompts:          pe,
			WebSearchEnabled: func() bool { return webEnabled },
			LoadedTools: func(string) map[string]bool {
				return map[string]bool{"http_request": true, "fetch_url": true}
			},
		})
		sess := &api.Session{ID: "loaded", AgentType: "coordinator", Posture: api.SessionPostureBuild, WorkspacePath: t.TempDir()}
		frame := inject.CoordinatorTurnFrame{RunContext: api.CoordinatorRunContext{PhaseCoordinatorSurface: "implement_investigate"}}
		out, _, _, err := testPromptSurface(engine).renderTripartiteCoordinatorPrompt(t.Context(), pe, sess, frame, nil, "", nil,
			map[string]any{"root_count": 1, "project_dir": sess.WorkspacePath})
		testutil.FailErr(t, "render loaded tool guidance", err)
		if !strings.Contains(out, "Use `http_request` for APIs") {
			t.Fatal("loaded HTTP tool guidance missing")
		}
		if strings.Contains(out, "Use `fetch_url` for readable web research") != webEnabled {
			t.Fatalf("fetch guidance does not match web enabled=%v", webEnabled)
		}
	}
}
