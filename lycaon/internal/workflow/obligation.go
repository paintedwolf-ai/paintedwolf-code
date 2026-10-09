package workflow

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/workflow/catalog"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowgates "github.com/lycaon/lycaon/internal/workflow/gates"
	workflowphases "github.com/lycaon/lycaon/internal/workflow/phases"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
	"log/slog"
	"strings"
)

type Obligations struct {
	Gates    workflowgates.GateEvaluator
	Runs     runstate.RunsRepository
	Vars     *runstate.Variables
	Resolver *catalog.Resolver
	Phases   *workflowphases.Service
	Kinds    map[string]ObligationKind
}

// ObligationKind identifies one host-observed phase wait.
type ObligationKind interface {
	Kind() string
	// ValidateParams checks declared parameters.
	ValidateParams(params map[string]any) error
	// OnPhaseEnter starts or adopts run-bound work.
	OnPhaseEnter(ctx context.Context, run *api.WorkflowRun, projectDir string, params map[string]any) error
	// Status reports run-scoped ledger state for the obligation phase declares.
	Status(ctx context.Context, workflowRunID, phase string) (api.WorkflowRunObligation, error)
}

// Register adds a kind to the obligation registry.
func (m *Obligations) Register(kind ObligationKind) {
	if m == nil || kind == nil || strings.TrimSpace(kind.Kind()) == "" {
		return
	}
	if m.Kinds == nil {
		m.Kinds = map[string]ObligationKind{}
	}
	m.Kinds[kind.Kind()] = kind
}

// ObligationParams returns the parameters a run's phase declares for an
// obligation kind. The phase is named, not read from the run, so a status read
// racing a phase advance still resolves the phase it was asked about.
func (m *Obligations) ObligationParams(ctx context.Context, workflowRunID, phaseID, kind string) (map[string]any, error) {
	run, err := m.Runs.Get(ctx, workflowRunID)
	if err != nil {
		return nil, err
	}
	manifest, err := m.Resolver.ForRun(ctx, run)
	if err != nil {
		return nil, err
	}
	phase, ok := manifest.PhaseForRun(run, phaseID)
	if !ok {
		return nil, fmt.Errorf("workflow phase %q unavailable", phaseID)
	}
	for _, obligation := range phase.OnEnter.Obligations {
		if obligation.Kind == kind {
			return obligation.Params, nil
		}
	}
	return nil, fmt.Errorf("workflow phase %q has no %q obligation", phase.ID, kind)
}

// RecordObligationTerminal refreshes a gate after ledger work settles.
func (m *Obligations) RecordObligationTerminal(ctx context.Context, workflowRunID, kind string) error {
	if m == nil {
		return nil
	}
	runID := strings.TrimSpace(workflowRunID)
	kind = strings.TrimSpace(kind)
	if runID == "" || kind == "" {
		return nil
	}
	run, err := m.Runs.Get(ctx, runID)
	if err != nil || run == nil {
		return err
	}
	manifest, err := m.Resolver.ForRun(ctx, run)
	if err != nil {
		return err
	}
	def, ok := manifest.PhaseByID(run.CurrentPhase)
	if !ok || !workflowdef.PhaseHasGate(def, workflowdef.ObligationGateLeaf(kind)) {
		return nil
	}
	if err := m.stampObligationVars(ctx, run, def); err != nil {
		return err
	}
	_, err = m.Phases.TryAutoAdvance(ctx, run.ID)
	return err
}

// stampObligationFailed settles work that failed before enqueue.
func (m *Obligations) stampObligationFailed(ctx context.Context, run *api.WorkflowRun, kind string, cause error) error {
	if m == nil || run == nil || cause == nil {
		return nil
	}
	summary := map[string]any{
		"status": api.ObligationStatusFailed,
		"error":  strings.TrimSpace(cause.Error()),
	}
	_, err := m.Vars.Stamp(ctx, run.ID, func(_ context.Context, _ *api.WorkflowRun, vars map[string]any) (map[string]any, bool, error) {
		return runstate.SetHostVar(vars, runstate.ObligationsVarKey+"."+kind, summary), true, nil
	})
	return err
}

// stampObligationVars refreshes phase obligation summaries.
func (m *Obligations) stampObligationVars(ctx context.Context, run *api.WorkflowRun, def workflowdef.PhaseDef) error {
	if m == nil || run == nil || !def.HasOnEnterObligations() {
		return nil
	}
	_, err := m.Vars.Stamp(ctx, run.ID, func(ctx context.Context, run *api.WorkflowRun, vars map[string]any) (map[string]any, bool, error) {
		changed := false
		for _, ob := range def.OnEnter.Obligations {
			kind := m.Kinds[ob.Kind]
			if kind == nil {
				continue
			}
			status, err := kind.Status(ctx, run.ID, def.ID)
			if err != nil {
				return nil, false, err
			}
			summary := obligationSummaryVar(status)
			if status.Status == api.ObligationStatusEmpty && conditions.ObligationFailedInVars(vars, ob.Kind) {
				// Keep failures that have no ledger row.
				continue
			}
			vars = runstate.SetHostVar(vars, runstate.ObligationsVarKey+"."+ob.Kind, summary)
			changed = true
		}
		return vars, changed, nil
	})
	return err
}

// obligationSummaryVar flattens detail beside status and error.
func obligationSummaryVar(status api.WorkflowRunObligation) map[string]any {
	out := map[string]any{}
	for k, v := range status.Detail {
		if k == "status" || k == "error" {
			continue
		}
		out[k] = v
	}
	out["status"] = status.Status
	if strings.TrimSpace(status.Error) != "" {
		out["error"] = strings.TrimSpace(status.Error)
	}
	return out
}

// ObligationsFromVars returns the host-stamped obligation summaries for injects.
func ObligationsFromVars(vars map[string]any) map[string]any {
	raw, ok := vars[runstate.ObligationsVarKey].(map[string]any)
	if !ok || len(raw) == 0 {
		return nil
	}
	return raw
}

// TriggerOnEnter starts phase work and records status.
func (m *Obligations) TriggerOnEnter(ctx context.Context, run *api.WorkflowRun, projectDir string, def workflowdef.PhaseDef) {
	if m == nil || run == nil || !def.HasOnEnterObligations() {
		return
	}
	for _, ob := range def.OnEnter.Obligations {
		kind := m.Kinds[ob.Kind]
		if kind == nil {
			slog.WarnContext(ctx, "workflow phase declares unregistered obligation kind; gate will hold",
				"run_id", run.ID, "phase", def.ID, "kind", ob.Kind)
			continue
		}
		if err := kind.OnPhaseEnter(ctx, run, projectDir, ob.Params); err != nil {
			_ = m.stampObligationFailed(ctx, run, ob.Kind, err)
		}
	}
	_ = m.stampObligationVars(ctx, run, def)
}

// Status returns current host waits.
func (m *Obligations) Status(ctx context.Context, run *api.WorkflowRun, def workflowdef.PhaseDef) []api.WorkflowRunObligation {
	if m == nil || run == nil || !def.HasOnEnterObligations() {
		return nil
	}
	out := make([]api.WorkflowRunObligation, 0, len(def.OnEnter.Obligations))
	for _, ob := range def.OnEnter.Obligations {
		kind := m.Kinds[ob.Kind]
		if kind == nil {
			out = append(out, api.WorkflowRunObligation{
				Kind: ob.Kind, Status: api.ObligationStatusPending, Error: "obligation kind not registered",
			})
			continue
		}
		status, err := kind.Status(ctx, run.ID, def.ID)
		if err != nil {
			out = append(out, api.WorkflowRunObligation{
				Kind: ob.Kind, Status: api.ObligationStatusPending, Error: err.Error(),
			})
			continue
		}
		status.Kind = ob.Kind
		out = append(out, status)
	}
	return out
}
