package workflow

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/lycaon/lycaon/internal/scaffoldvars"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

func runHasBlueprint(run *api.WorkflowRun) bool {
	return run != nil && strings.TrimSpace(run.BlueprintPath) != ""
}

// ComputeSessionUI derives host-managed session chrome for GET /sessions/{id}.
func (m *RunManager) ComputeSessionUI(ctx context.Context, sessionID string) (*api.SessionUiState, error) {
	if m == nil {
		return nil, nil
	}
	ui := &api.SessionUiState{}
	pending, err := m.pendingWorkflowStart(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	ui.PendingWorkflowStart = pending
	return ui, nil
}

// ComputeRunUI derives host-managed workflow chrome for Den and assistants.
func (m *RunManager) ComputeRunUI(ctx context.Context, run *api.WorkflowRun) (*api.WorkflowRunUi, error) {
	if m == nil || run == nil {
		return nil, nil
	}
	ui := &api.WorkflowRunUi{}
	manifest, err := m.manifestForRun(ctx, run)
	if err != nil {
		return nil, err
	}
	ui.Request = manifest.Request.Summary()
	phase, ok := manifest.PhaseForRun(run, run.CurrentPhase)
	if !ok {
		return nil, fmt.Errorf("workflow %q has no current phase %q", run.WorkflowID, run.CurrentPhase)
	}
	ui.CurrentPhaseLabel = strings.TrimSpace(phase.ActivityLabel)
	if ui.CurrentPhaseLabel == "" {
		return nil, fmt.Errorf("workflow %q phase %q has no activity label", run.WorkflowID, run.CurrentPhase)
	}
	// A terminal phase can remain running until the next advance.
	ui.PhaseTerminal = phase.Terminal
	var vars map[string]any
	if v, err := m.Store.GetScaffoldVars(ctx, run.ID); err == nil {
		vars = v
		ui.RequestState, _ = requestStateFromVars(vars)
		m.reconcileCoordinatorAskProjection(ctx, run.SessionID, vars)
		ui.HumanApprovalAwaiting = scaffoldvars.HumanApprovalAwaiting(vars) && phase.HumanApproval != nil && runHasBlueprint(run)
		if pf, ok := PendingFeedbackFromVars(vars); ok {
			ui.PendingFeedback = &pf
		}
	}
	if err := m.attachChoiceTransitionsFromManifest(ctx, run, ui, vars, manifest); err != nil {
		return nil, err
	}
	ui.ReportAvailable = reportAvailable(run, manifest, vars)
	ui.PhaseObligations = m.PhaseObligationsUI(ctx, run, phase)
	ui.TopologyLegs = m.topologyLegsUI(ctx, run, manifest)
	if !runHasBlueprint(run) || m.BlueprintGet == nil {
		return ui, nil
	}
	plan, err := m.BlueprintGet.Get(ctx, run.ProjectID, strings.TrimSpace(run.BlueprintPath))
	if err == nil && plan != nil {
		at := plan.UpdatedAt.UTC()
		ui.PlanRevisionAt = &at
	}
	return ui, nil
}

// topologyLegsUI projects the run's planned legs. A read failure leaves the
// projection out rather than failing the run's chrome.
func (m *RunManager) topologyLegsUI(ctx context.Context, run *api.WorkflowRun, manifest workflowdef.Manifest) []api.WorkflowTopologyLeg {
	topologyID := strings.TrimSpace(manifest.Topology)
	if m.TopologyLegs == nil || topologyID == "" {
		return nil
	}
	legs, err := m.TopologyLegs.RunTopologyLegs(ctx, run, topologyID, manifest.TopologyStagePhases())
	if err != nil {
		slog.WarnContext(ctx, "project workflow topology legs", "run_id", run.ID, "topology", topologyID, "error", err)
		return nil
	}
	return legs
}

// runAwaitsHumanApproval combines persisted approval state with the phase definition.
// The result is valid only when err is nil.
func (m *RunManager) runAwaitsHumanApproval(ctx context.Context, run *api.WorkflowRun, vars map[string]any) (bool, error) {
	// An absent approval wait needs no manifest lookup.
	if !scaffoldvars.HumanApprovalAwaiting(vars) {
		return false, nil
	}
	return m.currentPhaseHasHumanApproval(ctx, run)
}

// currentPhaseHasHumanApproval checks the phase shape, not persisted approval vars.
func (m *RunManager) currentPhaseHasHumanApproval(ctx context.Context, run *api.WorkflowRun) (bool, error) {
	manifest, err := m.manifestForRun(ctx, run)
	if err != nil {
		return false, err
	}
	phase, ok := manifest.PhaseByID(run.CurrentPhase)
	return ok && phase.HumanApproval != nil, nil
}

func (m *RunManager) attachChoiceTransitionsFromManifest(ctx context.Context, run *api.WorkflowRun, ui *api.WorkflowRunUi, vars map[string]any, manifest workflowdef.Manifest) error {
	def, ok := manifest.PhaseByID(run.CurrentPhase)
	if !ok || len(def.Transitions) == 0 {
		return nil
	}
	if vars == nil {
		vars = map[string]any{}
	}
	for _, edge := range def.Transitions {
		if !transitionActorAllowed(edge, workflowdef.TransitionActorHuman) {
			continue
		}
		armed, err := m.choiceTransitionArmed(ctx, run, manifest, edge, vars)
		if err != nil {
			return err
		}
		label := strings.TrimSpace(edge.Label)
		if label == "" {
			label = edge.ID
		}
		ui.ChoiceTransitions = append(ui.ChoiceTransitions, api.ChoiceTransitionUi{
			ID:    edge.ID,
			Label: label,
			Armed: armed,
		})
	}
	return nil
}

func (m *RunManager) pendingWorkflowStart(ctx context.Context, sessionID string) (*api.PendingWorkflowStart, error) {
	active, err := m.Store.ActiveBySession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if active != nil {
		return nil, nil
	}
	if m.SessionScaffold == nil {
		return nil, nil
	}
	vars, err := m.SessionScaffold.GetVars(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	wfID, ver, ok := proposedWorkflow(vars)
	if !ok {
		return nil, nil
	}
	if !m.Manifests.CatalogStartable(wfID, ver) {
		return nil, nil
	}
	label := m.catalogWorkflowLabel(wfID, ver)
	if presetID := proposedPresetID(vars); presetID != "" {
		if preset, _, ok := m.Manifests.PresetByID(wfID, ver, presetID); ok {
			if name := strings.TrimSpace(preset.Name); name != "" {
				label = name
			}
		}
	}
	return &api.PendingWorkflowStart{
		WorkflowID:      wfID,
		WorkflowVersion: ver,
		PresetID:        proposedPresetID(vars),
		Label:           label,
	}, nil
}

func proposedPresetID(vars map[string]any) string {
	raw, _ := vars[sessionVarWorkflowStartProposed].(map[string]any)
	if raw == nil {
		return ""
	}
	id, _ := raw["preset_id"].(string)
	return strings.TrimSpace(id)
}

func (m *RunManager) catalogWorkflowLabel(workflowID, version string) string {
	if m != nil && m.Manifests != nil {
		if mf, err := m.Manifests.Get(workflowID, version); err == nil {
			if name := strings.TrimSpace(mf.Name); name != "" {
				return name
			}
		}
	}
	return workflowID
}

// AttachRunUI sets run.UI in place when computable.
func (m *RunManager) AttachRunUI(ctx context.Context, run *api.WorkflowRun) error {
	if run == nil {
		return nil
	}
	ui, err := m.ComputeRunUI(ctx, run)
	if err != nil {
		return err
	}
	run.UI = ui
	return nil
}
