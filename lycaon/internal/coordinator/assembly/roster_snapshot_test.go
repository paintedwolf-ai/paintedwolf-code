package assembly

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestRosterRenderingReusesAvailabilityFacts(t *testing.T) {
	engine := &AssemblyEngine{}
	engine.SetDeps(AssemblyDeps{
		Injects: prompts.NewInjectRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{ModuleRoot: kickTestRoot(t)})),
		RepoKnownEmpty: func(context.Context, string) bool {
			t.Error("rendering re-read repository availability")
			return true
		},
		WebSearchEnabled: func() bool {
			t.Error("rendering re-read the web search gate")
			return false
		},
	})
	roster := inject.ResolveAgentRoster("implement_investigate", []string{"repo-researcher"}, 1, false, true)
	frame := inject.CoordinatorTurnFrame{Roster: &roster}
	_, err := engine.prependCoordinatorRunInject(t.Context(), &api.Session{ID: "session", WorkspacePath: t.TempDir()},
		frame, nil, &TurnAssemblyScratch{}, nil)
	testutil.FailErr(t, "render measured roster", err)
}
