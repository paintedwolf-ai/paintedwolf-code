package phases

import (
	"context"
	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/observability"
	workflowcatalog "github.com/lycaon/lycaon/internal/workflow/catalog"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowgates "github.com/lycaon/lycaon/internal/workflow/gates"
	"github.com/lycaon/lycaon/internal/workflow/publication"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/internal/workflow/toolguard"
	"github.com/lycaon/lycaon/pkg/api"
)

type PhasePlans interface {
	SyncTranscriptForRun(context.Context, *api.WorkflowRun, bool) error
}
type PhaseApprovals interface {
	AutoApproveOnPhase(context.Context, *api.WorkflowRun, workflowdef.Manifest, workflowdef.PhaseDef, map[string]any) (map[string]any, error)
}
type PhaseFeedback interface {
	NotifyPending(context.Context, string, map[string]any)
}
type PhaseSettlement interface {
	InvokeOnPhaseEnter(context.Context, *api.WorkflowRun, workflowdef.PhaseDef) error
	ReconcileTerminalRun(context.Context, *api.WorkflowRun) error
}

// Phases owns phase progression, gate admission, and committed entry effects.
type Service struct {
	Runs              runstate.RunsRepository
	Vars              *runstate.Variables
	Journal           *runstate.Journal
	Resolver          *workflowcatalog.Resolver
	Sessions          toolguard.Sessions
	Gates             workflowgates.GateEvaluator
	Registry          *conditions.ConditionRegistry
	Publication       *publication.Runs
	Plans             PhasePlans
	Approvals         PhaseApprovals
	Feedback          PhaseFeedback
	Settlement        PhaseSettlement
	Entries           *Entries
	ReviewSpawnFilter func(context.Context, string, string, []string) []string
	PhaseEnterHook    PhaseEnterHook
	PhaseReenterHook  PhaseReenterHook
}

var workflowStartLog = observability.LazyComponent("workflow_start")

func (m *Service) gateEvaluator() workflowgates.GateEvaluator {
	if m.Gates != nil {
		return m.Gates
	}
	return workflowgates.FailClosedGateEvaluator{}
}
func (m *Service) InitializeVars(ctx context.Context, sess *api.Session, run *api.WorkflowRun, manifest workflowdef.Manifest, vars map[string]any) (map[string]any, error) {
	return ApplyPhaseOnEnter(ctx, PhaseEnterRequest{Sessions: m.Sessions, SessionID: sess.ID, Manifest: manifest, PhaseID: run.CurrentPhase, Vars: vars, BlueprintPath: run.BlueprintPath, Registry: m.Registry, ReviewSpawnFilter: m.ReviewSpawnFilter})
}
