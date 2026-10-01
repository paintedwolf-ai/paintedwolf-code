package contract

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/workflow"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// Surface cards and phase exits render catalog templates over host facts.

func bundledPromptEngine() *prompts.FileTemplateEngine {
	return prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
}

// renderSurfaceCardFor renders the turn banner for one coordinator surface.
func renderSurfaceCardFor(t *testing.T, surfaceID string, sticky, deferred []string) string {
	t.Helper()
	label, rule, err := prompts.LoadCoordinatorSurfaceCard(surfaceID)
	contractcheck.FailErr(t, "load surface card for "+surfaceID, err)
	vars := prompts.CoordinatorSurfaceCardVars(label, rule, sticky, deferred).TemplateVars()
	out, err := bundledPromptEngine().Render(context.Background(),
		"partials/coordinator-surface-card.md", vars)
	contractcheck.FailErr(t, "render surface card for "+surfaceID, err)
	return out
}

// renderPhaseExitBlock renders one projected phase exit.
func renderPhaseExitBlock(t *testing.T, view workflow.PhaseExitView) string {
	t.Helper()
	vars := inject.ActiveWorkflowInjectToMap(inject.ActiveWorkflowInjectData{
		WorkflowID: "contract", CurrentPhase: "p", PhaseExit: view.InjectView(),
		Phases: []inject.ActiveWorkflowPhaseView{{ID: "p", IsCurrent: true}},
	}, nil, nil)
	out, err := bundledPromptEngine().Render(context.Background(), "guidance/active-workflow.md",
		vars)
	contractcheck.FailErr(t, "render phase exit block", err)
	return out
}
