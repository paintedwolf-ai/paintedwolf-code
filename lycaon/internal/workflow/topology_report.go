package workflow

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/workflow/catalog"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/internal/workflow/lifecycle"
	workflowphases "github.com/lycaon/lycaon/internal/workflow/phases"
	workflowreview "github.com/lycaon/lycaon/internal/workflow/review"
	runstate "github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
)

type reportSettlement int

const (
	reportUnsettled reportSettlement = iota
	reportAccepted
	reportNotAccepted
)

// MaybeDeliverTopologyReport settles the report phase on a committed report:
// an accepted one satisfies topology_report_delivered and advances committed
// gates, and one stored with document defects fails the run as not accepted.

type Reports struct {
	Coverage *workflowreview.Coverage
	Verdicts *workflowreview.Verdicts
	Runs     runstate.RunsRepository
	Sessions session.Store
	Resolver *catalog.Resolver
	Vars     *runstate.Variables
	Controls *lifecycle.Commands
	Phases   *workflowphases.Service
}

func (m *Reports) MaybeDeliverTopologyReport(ctx context.Context, sessionID, messageID string) error {
	if m == nil || strings.TrimSpace(sessionID) == "" || strings.TrimSpace(messageID) == "" {
		return nil
	}
	message, err := m.Sessions.GetMessage(ctx, sessionID, messageID)
	if err != nil {
		return err
	}
	active, err := m.Runs.ActiveBySession(ctx, sessionID)
	if err != nil || active == nil {
		return err
	}
	manifest, err := m.Resolver.ForRun(ctx, active)
	if err != nil {
		return err
	}
	settled, phase := reportUnsettled, ""
	if _, err := m.Vars.Stamp(ctx, active.ID, func(ctx context.Context, run *api.WorkflowRun, vars map[string]any) (map[string]any, bool, error) {
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
		return runstate.SatisfyGateInVars(vars, "topology_report_delivered"), true, nil
	}); err != nil {
		return err
	}
	switch settled {
	case reportNotAccepted:
		_, err = m.Controls.Fail(ctx, active.ID, reportNotAcceptedFailure(message.CompletionReport.Defects, phase))
		return err
	case reportAccepted:
		// Entering done stamps terminal completion for the next hop.
		_, err = m.Phases.TryAutoAdvanceThroughCommittedGates(ctx, active.ID, len(manifest.Phases))
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
		Code: runstate.ReportNotAcceptedFailureCode,
		Message: fmt.Sprintf("The report still failed %d document %s when its repairs ran out. It was stored with what it is missing.",
			len(defects), requirements),
		Phase: phase,
	}
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
func (m *Reports) RecoverReportDelivery(ctx context.Context, run *api.WorkflowRun) (bool, error) {
	manifest, err := m.Resolver.ForRun(ctx, run)
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
