package workflow

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/lycaon/lycaon/internal/conditions"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

const obligationsVarKey = "obligations"

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

// EvidenceDigestSource contributes one block to the review-loop evidence digest.
type EvidenceDigestSource func(ctx context.Context, workflowRunID string) string

// RegisterObligationKind adds a kind to the manager's registry.
func (m *RunManager) RegisterObligationKind(kind ObligationKind) {
	if m == nil || kind == nil || strings.TrimSpace(kind.Kind()) == "" {
		return
	}
	if m.Obligations == nil {
		m.Obligations = map[string]ObligationKind{}
	}
	m.Obligations[kind.Kind()] = kind
}

// ObligationGateLeaf returns the settled gate leaf id for a kind.
func ObligationGateLeaf(kind string) string {
	return conditions.ObligationGatePrefix + strings.TrimSpace(kind)
}

// ObligationKindFromGateLeaf extracts the kind from obligation_settled:<kind>.
func ObligationKindFromGateLeaf(leaf string) (string, bool) {
	leaf = strings.TrimSpace(leaf)
	if !strings.HasPrefix(leaf, conditions.ObligationGatePrefix) {
		return "", false
	}
	kind := strings.TrimSpace(strings.TrimPrefix(leaf, conditions.ObligationGatePrefix))
	return kind, kind != ""
}

// ObligationParams returns the parameters a run's phase declares for an
// obligation kind. The phase is named, not read from the run, so a status read
// racing a phase advance still resolves the phase it was asked about.
func (m *RunManager) ObligationParams(ctx context.Context, workflowRunID, phaseID, kind string) (map[string]any, error) {
	run, err := m.Get(ctx, workflowRunID)
	if err != nil {
		return nil, err
	}
	manifest, err := m.manifestForRun(ctx, run)
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
func (m *RunManager) RecordObligationTerminal(ctx context.Context, workflowRunID, kind string) error {
	if m == nil {
		return nil
	}
	runID := strings.TrimSpace(workflowRunID)
	kind = strings.TrimSpace(kind)
	if runID == "" || kind == "" {
		return nil
	}
	run, err := m.Get(ctx, runID)
	if err != nil || run == nil {
		return err
	}
	manifest, err := m.manifestForRun(ctx, run)
	if err != nil {
		return err
	}
	def, ok := manifest.PhaseByID(run.CurrentPhase)
	if !ok || !workflowdef.PhaseHasGate(def, ObligationGateLeaf(kind)) {
		return nil
	}
	if err := m.stampObligationVars(ctx, run, def); err != nil {
		return err
	}
	_, err = m.TryAutoAdvance(ctx, run.ID)
	return err
}

// stampObligationFailed settles work that failed before enqueue.
func (m *RunManager) stampObligationFailed(ctx context.Context, run *api.WorkflowRun, kind string, cause error) error {
	if m == nil || run == nil || cause == nil {
		return nil
	}
	summary := map[string]any{
		"status": api.ObligationStatusFailed,
		"error":  strings.TrimSpace(cause.Error()),
	}
	_, err := m.StampRunVars(ctx, run.ID, func(_ context.Context, _ *api.WorkflowRun, vars map[string]any) (map[string]any, bool, error) {
		return SetHostVar(vars, obligationsVarKey+"."+kind, summary), true, nil
	})
	return err
}

// stampObligationVars refreshes phase obligation summaries.
func (m *RunManager) stampObligationVars(ctx context.Context, run *api.WorkflowRun, def workflowdef.PhaseDef) error {
	if m == nil || run == nil || !def.HasOnEnterObligations() {
		return nil
	}
	_, err := m.StampRunVars(ctx, run.ID, func(ctx context.Context, run *api.WorkflowRun, vars map[string]any) (map[string]any, bool, error) {
		changed := false
		for _, ob := range def.OnEnter.Obligations {
			kind := m.Obligations[ob.Kind]
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
			vars = SetHostVar(vars, obligationsVarKey+"."+ob.Kind, summary)
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
	raw, ok := vars[obligationsVarKey].(map[string]any)
	if !ok || len(raw) == 0 {
		return nil
	}
	return raw
}

// triggerObligationsOnEnter starts phase work and records status.
func (m *RunManager) triggerObligationsOnEnter(ctx context.Context, run *api.WorkflowRun, projectDir string, def workflowdef.PhaseDef) {
	if m == nil || run == nil || !def.HasOnEnterObligations() {
		return
	}
	for _, ob := range def.OnEnter.Obligations {
		kind := m.Obligations[ob.Kind]
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

// PhaseObligationsUI returns current host waits.
func (m *RunManager) PhaseObligationsUI(ctx context.Context, run *api.WorkflowRun, def workflowdef.PhaseDef) []api.WorkflowRunObligation {
	if m == nil || run == nil || !def.HasOnEnterObligations() {
		return nil
	}
	out := make([]api.WorkflowRunObligation, 0, len(def.OnEnter.Obligations))
	for _, ob := range def.OnEnter.Obligations {
		kind := m.Obligations[ob.Kind]
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
