package contract

import (
	"context"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/prompts"
	workflowpresentation "github.com/lycaon/lycaon/internal/workflow/presentation"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"testing"
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
func renderPhaseExitBlock(t *testing.T, view workflowpresentation.PhaseExitView) string {
	t.Helper()
	return renderPhaseExitView(t, view.InjectView())
}

// renderPhaseExitView renders the phase-exit block for a projected view.
func renderPhaseExitView(t *testing.T, view *inject.PhaseExitView) string {
	t.Helper()
	vars := inject.ActiveWorkflowInjectToMap(inject.ActiveWorkflowInjectData{
		WorkflowID: "contract", CurrentPhase: "p", PhaseExit: view,
		Phases: []inject.ActiveWorkflowPhaseView{{ID: "p", IsCurrent: true}},
	}, nil, nil)
	out, err := bundledPromptEngine().Render(context.Background(), "guidance/active-workflow.md",
		vars)
	contractcheck.FailErr(t, "render phase exit block", err)
	return out
}
