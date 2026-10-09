package phases_test

import (
	workflow "github.com/lycaon/lycaon/internal/workflow"

	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

type stubWorkflowObligation struct {
	status   api.WorkflowRunObligation
	enterErr error
	onEnter  func(*api.WorkflowRun)
}

func (s *stubWorkflowObligation) Kind() string { return "scan" }

func (s *stubWorkflowObligation) ValidateParams(map[string]any) error { return nil }

func (s *stubWorkflowObligation) OnPhaseEnter(_ context.Context, run *api.WorkflowRun, _ string, _ map[string]any) error {
	if s.onEnter != nil {
		s.onEnter(run)
	}
	return s.enterErr
}

func (s *stubWorkflowObligation) Status(context.Context, string, string) (api.WorkflowRunObligation, error) {
	return s.status, nil
}

func obligationTestManifest() workflowdef.Manifest {
	return workflowdef.FinalizeManifest(workflowdef.Manifest{
		ID:       "obligationtest",
		Version:  "1.0.0",
		Controls: workflowdef.ManifestControls{PhaseAdvance: workflowdef.PhaseAdvanceHost},
		PhaseDefs: []workflowdef.PhaseDef{
			{
				ID:           "ingest",
				CompleteWhen: workflowdef.CompleteWhenGatesSatisfied,
				Gates:        []string{workflowdef.ObligationGateLeaf("scan")},
				OnEnter: workflowdef.PhaseOnEnter{Obligations: []workflowdef.ObligationDef{{
					Kind: "scan", Params: map[string]any{"categories": []any{"security"}},
				}}},
				Next: "done",
			},
			{ID: "done", Terminal: true, CompleteWhen: "orchestration_complete"},
		},
	})
}

func wireTestObligation(t *testing.T, mgr *workflow.RunManager, obligation *stubWorkflowObligation) {
	t.Helper()
	mgr.Obligations.Register(obligation)
	deps := conditions.TestRegistryDeps()
	deps.ObligationResolvers = map[string]conditions.ObligationStatusReader{"scan": obligation}
	reg, err := conditions.NewDefaultRegistry(deps)
	testutil.FailErr(t, "conditions registry", err)
	mgr.SetConditionRegistry(reg)
}
