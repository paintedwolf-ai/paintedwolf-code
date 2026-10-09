package session

import (
	"context"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/pkg/api"
	"strings"
)

var coordinatorMutationTools = map[string]struct{}{
	"write":         {},
	"edit":          {},
	"replace_lines": {},
	"code_rewrite":  {},
	"jq_edit":       {},
	"chmod":         {},
	"delete":        {},
}

// BatchReadyForSynthesis checks every host wrapup gate.
func BatchReadyForSynthesis(state surface.ImplementSessionState, history []api.Message, progressContent string, verifyRequired, verifyOK bool) bool {
	if !batchReadyIgnoringProgress(state, history, verifyRequired, verifyOK) {
		return false
	}
	return progress.AllTerminal(progressContent)
}

// SynthesisBlockedOnlyByOpenProgress isolates the open-progress gate.
func SynthesisBlockedOnlyByOpenProgress(state surface.ImplementSessionState, history []api.Message, progressContent string, verifyRequired, verifyOK bool) bool {
	if !batchReadyIgnoringProgress(state, history, verifyRequired, verifyOK) {
		return false
	}
	return !progress.AllTerminal(progressContent) && !progress.ProgressMissing(progressContent)
}

// batchReadyIgnoringProgress checks every gate except progress completion.
func batchReadyIgnoringProgress(state surface.ImplementSessionState, history []api.Message, verifyRequired, verifyOK bool) bool {
	since := api.UserIntentBoundary(history)
	if state.WorkersInFlight != 0 {
		return false
	}
	if len(state.PendingOverlayIDs) > 0 {
		return false
	}
	if OpenWorkerEnvelopeSince(history, since) {
		return false
	}
	if !terminalEnvelopesLegCompleteSince(history, since) {
		return false
	}
	if verifyRequired && !verifyOK {
		return false
	}
	return true
}

// OpenRepairSinceUserIntent reports unresolved worker or verification failures.
func OpenRepairSinceUserIntent(history []api.Message, verifyRepair bool) bool {
	since := api.UserIntentBoundary(history)
	for i := since; i < len(history); i++ {
		if summary := history[i].WorkerSummary; summary != nil &&
			(summary.Status == api.WorkerSummaryStatusPartial || summary.Status == api.WorkerSummaryStatusFailed) {
			return true
		}
	}
	for _, env := range TerminalWorkerEnvelopesSince(history, since) {
		leg := envelopeLegStatus(env)
		if leg == "partial" || leg == "blocked" {
			return true
		}
	}
	return verifyRepair
}

// ImplementationWorkSinceBoundary reports mutations after a transcript boundary.
func ImplementationWorkSinceBoundary(history []api.Message, sinceIdx int) bool {
	for i := sinceIdx; i < len(history); i++ {
		msg := history[i]
		if msg.Role != api.MessageRoleTool || msg.ToolResult == nil {
			continue
		}
		if msg.ToolResult.Outcome != api.ToolResultOutcomeCompleted {
			continue
		}
		if msg.ToolResult.FileEdit != nil {
			return true
		}
		if _, ok := coordinatorMutationTools[strings.TrimSpace(msg.ToolResult.Tool)]; ok {
			return true
		}
	}
	for _, env := range TerminalWorkerEnvelopesSince(history, sinceIdx) {
		if len(env.Report.FilesModified) > 0 {
			return true
		}
	}
	return false
}

func (m *Manager) activePhaseRequiresVerify(ctx context.Context, sessionID string) bool {
	if m == nil || m.workflows == nil {
		return false
	}
	return m.workflows.Policy.ActivePhaseRequiresEvidence(ctx, sessionID, "verify")
}

// Only resumable states keep a worker envelope open.
var openWorkerEnvelopeStates = map[string]struct{}{
	string(api.WorkerSummaryStatusOpen):          {},
	string(api.WorkerSummaryStatusNeedsDecision): {},
	string(api.WorkerSummaryStatusHeld):          {},
}

// OpenWorkerEnvelopeSince reports worker states that block wrap-up.
func OpenWorkerEnvelopeSince(history []api.Message, sinceIdx int) bool {
	for i := sinceIdx; i < len(history); i++ {
		env, ok := parseMessageWorkerEnvelope(history[i])
		if !ok {
			continue
		}
		state := strings.ToLower(strings.TrimSpace(env.State))
		if _, open := openWorkerEnvelopeStates[state]; open {
			return true
		}
	}
	return false
}

func terminalEnvelopesLegCompleteSince(history []api.Message, sinceIdx int) bool {
	for _, env := range TerminalWorkerEnvelopesSince(history, sinceIdx) {
		leg := envelopeLegStatus(env)
		if leg != "complete" {
			return false
		}
	}
	return true
}

// TerminalWorkerEnvelopesSince returns worker completion envelopes since sinceIdx.
func TerminalWorkerEnvelopesSince(history []api.Message, sinceIdx int) []WorkerCompletionEnvelope {
	var out []WorkerCompletionEnvelope
	for i := sinceIdx; i < len(history); i++ {
		if !surface.TerminalWorkerCompletionMessage(history[i]) {
			continue
		}
		env, ok := parseMessageWorkerEnvelope(history[i])
		if !ok {
			continue
		}
		out = append(out, env)
	}
	return out
}

func parseMessageWorkerEnvelope(msg api.Message) (WorkerCompletionEnvelope, bool) {
	if msg.WorkerSummary == nil {
		return WorkerCompletionEnvelope{}, false
	}
	return ParseWorkerCompletionEnvelope(msg.WorkerSummary.Envelope)
}

func envelopeLegStatus(env WorkerCompletionEnvelope) string {
	if leg := strings.ToLower(strings.TrimSpace(env.Report.LegStatus)); leg != "" {
		return leg
	}
	return strings.ToLower(strings.TrimSpace(env.State))
}
