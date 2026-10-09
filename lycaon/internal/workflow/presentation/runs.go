package presentation

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/lycaon/lycaon/internal/scaffoldvars"
	workflowblueprintfiles "github.com/lycaon/lycaon/internal/workflow/blueprintfiles"
	workflowcatalog "github.com/lycaon/lycaon/internal/workflow/catalog"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
)

// ComputeSessionUI derives host-managed session chrome for GET /sessions/{id}.
func (m *Runs) ComputeSessionUI(ctx context.Context, sessionID string) (*api.SessionUiState, error) {
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
func (m *Runs) ComputeRunUI(ctx context.Context, run *api.WorkflowRun) (*api.WorkflowRunUi, error) {
	if m == nil || run == nil {
		return nil, nil
	}
	ui := &api.WorkflowRunUi{}
	manifest, err := m.Resolver.ForRun(ctx, run)
	if err != nil {
		return nil, err
	}
	summary := manifest.Summary()
	ui.Definition = &summary
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
	if v, err := m.Reader.GetScaffoldVars(ctx, run.ID); err == nil {
		vars = v
		ui.RequestState, _ = runstate.RequestStateFromVars(vars)
		m.Asks.ReconcileCoordinatorAskProjection(ctx, run.SessionID, vars)
		ui.HumanApprovalAwaiting = scaffoldvars.HumanApprovalAwaiting(vars) && phase.HumanApproval != nil && runstate.RunHasBlueprint(run)
		if pf, ok := runstate.PendingFeedbackFromVars(vars); ok {
			ui.PendingFeedback = &pf
		}
	}
	if err := m.attachChoiceTransitionsFromManifest(ctx, run, ui, vars, manifest); err != nil {
		return nil, err
	}
	ui.ReportAvailable = reportAvailable(run, manifest, vars)
	ui.PhaseObligations = m.Obligations.Status(ctx, run, phase)
	ui.TopologyLegs = m.topologyLegsUI(ctx, run, manifest)
	if !runstate.RunHasBlueprint(run) || m.BlueprintGetter == nil {
		return ui, nil
	}
	plan, err := m.BlueprintGetter.Get(ctx, run.ProjectID, strings.TrimSpace(run.BlueprintPath))
	if err == nil && plan != nil {
		at := plan.UpdatedAt.UTC()
		ui.PlanRevisionAt = &at
	}
	return ui, nil
}

// Unavailable topology details leave the rest of the run view intact.
func (m *Runs) topologyLegsUI(ctx context.Context, run *api.WorkflowRun, manifest workflowdef.Manifest) []api.WorkflowTopologyLeg {
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

// currentPhaseHasHumanApproval checks the phase shape, not persisted approval vars.

func (m *Runs) attachChoiceTransitionsFromManifest(ctx context.Context, run *api.WorkflowRun, ui *api.WorkflowRunUi, vars map[string]any, manifest workflowdef.Manifest) error {
	def, ok := manifest.PhaseByID(run.CurrentPhase)
	if !ok || len(def.Transitions) == 0 {
		return nil
	}
	if vars == nil {
		vars = map[string]any{}
	}
	for _, edge := range def.Transitions {
		if !workflowdef.TransitionActorAllowed(edge, workflowdef.TransitionActorHuman) {
			continue
		}
		armed, err := m.Choices.ChoiceTransitionArmed(ctx, run, manifest, edge, vars)
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

func (m *Runs) pendingWorkflowStart(ctx context.Context, sessionID string) (*api.PendingWorkflowStart, error) {
	active, err := m.Reader.ActiveBySession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if active != nil {
		return nil, nil
	}
	if m.Scaffold == nil {
		return nil, nil
	}
	wfID, ver, presetID, ok, err := m.Scaffold.ProposedStart(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	if !m.Resolver.Overlay.CatalogStartable(wfID, ver) {
		return nil, nil
	}
	label := m.catalogWorkflowLabel(wfID, ver)
	if presetID != "" {
		if preset, _, ok := m.Resolver.Overlay.PresetByID(wfID, ver, presetID); ok {
			if name := strings.TrimSpace(preset.Name); name != "" {
				label = name
			}
		}
	}
	return &api.PendingWorkflowStart{
		WorkflowID:      wfID,
		WorkflowVersion: ver,
		PresetID:        presetID,
		Label:           label,
	}, nil
}

func (m *Runs) catalogWorkflowLabel(workflowID, version string) string {
	if m != nil && m.Resolver.Overlay != nil {
		if mf, err := m.Resolver.Overlay.Get(workflowID, version); err == nil {
			if name := strings.TrimSpace(mf.Name); name != "" {
				return name
			}
		}
	}
	return workflowID
}

// AttachRunUI sets run.UI in place when computable.
func (m *Runs) AttachRunUI(ctx context.Context, run *api.WorkflowRun) error {
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

type Runs struct {
	Reader          runstate.RunsRepository
	Resolver        *workflowcatalog.Resolver
	Scaffold        StartProposal
	Asks            AskProjection
	BlueprintGetter workflowblueprintfiles.Getter
	TopologyLegs    TopologyLegSource
	Obligations     ObligationStatus
	Choices         ChoiceTransitions
}
