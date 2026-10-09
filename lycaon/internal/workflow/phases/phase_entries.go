package phases

import (
	"context"
	"log/slog"

	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
)

type PhaseObligations interface {
	TriggerOnEnter(context.Context, *api.WorkflowRun, string, workflowdef.PhaseDef)
}
type PhaseExplanations interface {
	Append(context.Context, *api.WorkflowRun, workflowdef.PhaseDef)
}
type PhaseReviews interface {
	StampEvidence(context.Context, *api.WorkflowRun) error
}
type Entries struct {
	Blueprints   PhasePlans
	Obligations  PhaseObligations
	Explanations PhaseExplanations
	Reviews      PhaseReviews
}

func (m *Entries) Trigger(ctx context.Context, run *api.WorkflowRun, projectDir string, def workflowdef.PhaseDef) {
	if m == nil {
		return
	}
	if def.HumanApproval != nil && runstate.RunHasBlueprint(run) {
		if err := m.Blueprints.SyncTranscriptForRun(ctx, run, false); err != nil {
			slog.WarnContext(ctx, "sync blueprint approval transcript", "run_id", run.ID, "phase", def.ID, "error", err)
		}
	}
	m.Obligations.TriggerOnEnter(ctx, run, projectDir, def)
	m.Explanations.Append(ctx, run, def)
	if def.ReviewLoop != nil {
		if err := m.Reviews.StampEvidence(ctx, run); err != nil {
			slog.WarnContext(ctx, "stamp review loop evidence", "run_id", run.ID, "error", err)
		}
	}
}
