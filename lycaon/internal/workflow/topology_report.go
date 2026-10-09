package workflow

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/guidance"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

// ReportNotAcceptedFailureCode ends a run whose report phase stored a report
// that still failed the document check when its repairs ran out.
const ReportNotAcceptedFailureCode = "REPORT_NOT_ACCEPTED"

type reportSettlement int

const (
	reportUnsettled reportSettlement = iota
	reportAccepted
	reportNotAccepted
)

// MaybeDeliverTopologyReport settles the report phase on a committed report:
// an accepted one satisfies topology_report_delivered and advances committed
// gates, and one stored with document defects fails the run as not accepted.
func (m *RunManager) MaybeDeliverTopologyReport(ctx context.Context, sessionID, messageID string) error {
	if m == nil || strings.TrimSpace(sessionID) == "" || strings.TrimSpace(messageID) == "" {
		return nil
	}
	message, err := m.Sessions.GetMessage(ctx, sessionID, messageID)
	if err != nil {
		return err
	}
	active, err := m.Store.ActiveBySession(ctx, sessionID)
	if err != nil || active == nil {
		return err
	}
	manifest, err := m.manifestForRun(ctx, active)
	if err != nil {
		return err
	}
	settled, phase := reportUnsettled, ""
	if _, err := m.StampRunVars(ctx, active.ID, func(ctx context.Context, run *api.WorkflowRun, vars map[string]any) (map[string]any, bool, error) {
		settled = reportUnsettled
		def, ok := manifest.PhaseForRun(run, run.CurrentPhase)
		if !ok || !workflowdef.PhaseHasGate(def, "topology_report_delivered") ||
			run.Status != api.WorkflowRunStatusRunning || !reportPhaseCompletion(message, run, manifest) {
			return nil, false, nil
		}
		phase = run.CurrentPhase
		hasFatal := false
		for _, defect := range message.CompletionReport.Defects {
			if guidance.IsFatalReportDefect(string(defect.Code)) {
				hasFatal = true
				break
			}
		}
		if hasFatal {
			settled = reportNotAccepted
			return nil, false, nil
		}
		settled = reportAccepted
		return SatisfyGateInVars(vars, "topology_report_delivered"), true, nil
	}); err != nil {
		return err
	}
	switch settled {
	case reportNotAccepted:
		_, err = m.Fail(ctx, active.ID, reportNotAcceptedFailure(message.CompletionReport.Defects, phase))
		return err
	case reportAccepted:
		// Entering done stamps terminal completion for the next hop.
		_, err = m.TryAutoAdvanceThroughCommittedGates(ctx, active.ID, len(manifest.Phases))
		return err
	default:
		return nil
	}
}

func reportNotAcceptedFailure(defects []api.CompletionReportDefect, phase string) api.WorkflowFailure {
	requirements := "requirement"
	if len(defects) != 1 {
		requirements = "requirements"
	}
	return api.WorkflowFailure{
		Code: ReportNotAcceptedFailureCode,
		Message: fmt.Sprintf("The report still failed %d document %s when its repairs ran out. It was stored with what it is missing.",
			len(defects), requirements),
		Phase: phase,
	}
}

// reportNotAcceptedRun reports whether a run ended on a report the host
// stored without accepting it.
func reportNotAcceptedRun(run *api.WorkflowRun) bool {
	return run != nil && run.Status == api.WorkflowRunStatusFailed &&
		run.Failure != nil && run.Failure.Code == ReportNotAcceptedFailureCode
}

// reportPhaseCompletion binds delivery to a committed completion of this phase.
// Workflows without document output still require their ordinary grounded reply.
func reportPhaseCompletion(message api.Message, run *api.WorkflowRun, manifest workflowdef.Manifest) bool {
	if message.Role != api.MessageRoleAssistant || message.Kind != api.MessageKindCompletionReport ||
		message.Grounding == nil || message.WorkflowRunID != run.ID || len(message.ToolCalls) != 0 ||
		(message.DraftStatus != "" && message.DraftStatus != api.DraftStatusCommitted) ||
		(message.Visibility != "" && message.Visibility != api.MessageVisibilityTranscript) {
		return false
	}
	meta := message.CompletionReport
	if meta == nil || meta.Phase != run.CurrentPhase {
		return false
	}
	if manifest.ReportEnabled() {
		return meta.Scope == api.CompletionReportScopeRun
	}
	return meta.Scope == api.CompletionReportScopePhase
}

// recoverReportDelivery finishes a committed closeout after process loss.
func (m *RunManager) recoverReportDelivery(ctx context.Context, run *api.WorkflowRun) (bool, error) {
	manifest, err := m.manifestForRun(ctx, run)
	if err != nil {
		return false, err
	}
	phase, ok := manifest.PhaseForRun(run, run.CurrentPhase)
	if !ok || !workflowdef.PhaseHasGate(phase, "topology_report_delivered") {
		return false, nil
	}
	messages, err := m.Sessions.GetMessages(ctx, run.SessionID)
	if err != nil {
		return false, err
	}
	for i := len(messages) - 1; i >= 0; i-- {
		if reportPhaseCompletion(messages[i], run, manifest) {
			return true, m.MaybeDeliverTopologyReport(ctx, run.SessionID, messages[i].ID)
		}
	}
	return false, nil
}

// MarkTopologyStageComplete records stage output for topology gates.
func (m *RunManager) MarkTopologyStageComplete(ctx context.Context, runID, stage, output, designForkCriterion string) error {
	if m == nil {
		return fmt.Errorf("workflow manager not configured")
	}
	stage = strings.TrimSpace(stage)
	if stage == "" {
		return fmt.Errorf("empty topology stage")
	}
	if _, err := m.StampRunVars(ctx, runID, func(_ context.Context, _ *api.WorkflowRun, vars map[string]any) (map[string]any, bool, error) {
		vars = markTopologyStage(vars, stage, output)
		if c := strings.TrimSpace(designForkCriterion); c != "" {
			vars = SetHostVar(vars, "options.criterion", c)
			vars = SetHostVar(vars, "artifact.selection.criterion", c)
		}
		return vars, true, nil
	}); err != nil {
		return err
	}
	_, _ = m.TryAutoAdvance(ctx, runID)
	_, _ = m.advanceTopologyBoundPhaseIfReady(ctx, runID, stage)
	return nil
}

// advanceTopologyBoundPhaseIfReady advances a satisfied topology phase.
func (m *RunManager) advanceTopologyBoundPhaseIfReady(ctx context.Context, runID, stage string) (*api.WorkflowRun, error) {
	run, err := m.loadRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	if IsTerminal(run.Status) || run.Status == api.WorkflowRunStatusPaused {
		return run, nil
	}
	manifest, err := m.manifestForRun(ctx, run)
	if err != nil {
		return nil, err
	}
	def, ok := manifest.PhaseByID(run.CurrentPhase)
	if !ok {
		return run, nil
	}
	bound := strings.TrimSpace(def.BindTopologyStage) == stage
	if !bound && len(def.BindParallelGroup) > 0 {
		for _, name := range def.BindParallelGroup {
			if strings.TrimSpace(name) == stage {
				bound = true
				break
			}
		}
	}
	if !bound {
		return run, nil
	}
	vars, err := m.Store.GetScaffoldVars(ctx, runID)
	if err != nil {
		return nil, err
	}
	okGate, _, err := m.gateEvaluator().PhaseGateMet(ctx, manifest, run, vars)
	if err != nil || !okGate {
		return run, nil //nolint:nilerr // gate failure is not fatal for topology host advance hook
	}
	return m.Advance(ctx, runID)
}

func markTopologyStage(vars map[string]any, stage, output string) map[string]any {
	vars = cloneVars(vars)
	stages, _ := vars["topology_stages"].(map[string]any)
	if stages == nil {
		stages = map[string]any{}
		vars["topology_stages"] = stages
	}
	entry := map[string]any{"complete": true}
	output = strings.TrimSpace(output)
	if output != "" {
		entry["output"] = output
		vars = SetHostVar(vars, "topology_outputs."+stage, output)
	}
	stages[stage] = entry
	return vars
}
