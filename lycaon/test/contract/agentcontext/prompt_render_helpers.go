package contract

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/prompts"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// Surface cards and phase exits render catalog templates over host facts.

func bundledPromptEngine() *prompts.FileTemplateEngine {
	return prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
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
