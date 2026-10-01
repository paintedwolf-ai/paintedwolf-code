package contract

import (
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/guidance/feedback"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/workflow"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/workflowfixture"
)

// Measure live phase-exit shapes as well as the baseline research fixture, so
// required verdict fields cannot grow outside the measured prompt surface.
func largestCatalogPhaseInject(t *testing.T, renderer *prompts.InjectRenderer, hints *guidance.HintConfig, gates *feedback.GateFeedbackCatalog, largest string) string {
	t.Helper()
	manifests, err := workflowfixture.LoadMergedWorkflowCatalog(t)
	contractcheck.FailErr(t, "load phase budget catalog", err)
	for _, manifest := range manifests {
		rows := make([]inject.WorkflowPhaseRow, 0, len(manifest.PhaseDefs))
		for _, phase := range manifest.PhaseDefs {
			rows = append(rows, inject.WorkflowPhaseRow{ID: phase.ID, CompleteWhen: phase.CompleteWhen, Next: phase.Next, Terminal: phase.Terminal})
		}
		for _, phase := range manifest.PhaseDefs {
			frame := inject.CoordinatorTurnFrame{
				RunContext: api.CoordinatorRunContext{WorkflowID: manifest.ID, CurrentPhase: phase.ID, RunID: "budget-run", RunStatus: "running"},
				Runtime:    inject.WorkflowRuntimeSnapshot{Phases: rows, PhaseExit: workflow.ProjectPhaseExit(manifest, phase, nil, nil).InjectView()},
			}
			block, err := inject.RenderActiveWorkflowInject(t.Context(), renderer, "sess-inject-test", frame, hints, nil, gates)
			contractcheck.FailErr(t, "render catalog phase budget", err)
			if len(block) > len(largest) {
				largest = block
			}
		}
	}
	return largest
}
