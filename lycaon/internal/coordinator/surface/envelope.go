package surface

import (
	"encoding/xml"
	"errors"
	"io"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/pkg/api"
)

// TaskEnvelopeAttributes parses the declared task envelope element without
// inferring state from surrounding assistant prose.
func TaskEnvelopeAttributes(content string) (map[string]string, bool) {
	decoder := xml.NewDecoder(strings.NewReader(strings.TrimSpace(content)))
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			return nil, false
		}
		if err != nil {
			return nil, false
		}
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != "task" {
			continue
		}
		attrs := make(map[string]string, len(start.Attr))
		for _, attr := range start.Attr {
			attrs[attr.Name.Local] = strings.TrimSpace(attr.Value)
		}
		return attrs, true
	}
}

const (
	HostLoopWakeSentinel = "[host:loop-wake]"
	// SurfaceImplementDispatch chains task() after read scouts or an idle implementer batch.
	SurfaceImplementDispatch = "implement_dispatch"
	// SurfaceImplementOverlayPromote waits for an explicit overlay decision.
	SurfaceImplementOverlayPromote = "implement_overlay_promote"
	// SurfaceImplementPark is host-cycle orchestration while sibling workers are still in flight.
	SurfaceImplementPark = "implement_park"
)

// ImplementSessionState carries host worker-cycle facts for coordinator surface selection.
type ImplementSessionState struct {
	// PendingOverlayIDs lists write-worker jobs awaiting integration.
	PendingOverlayIDs []string
	// PendingOverlayPaths lists observed changes in pending overlays.
	PendingOverlayPaths []string
	WorkersInFlight     int
	// BatchPhase and BatchSeq mirror the host batch state.
	BatchPhase string
	BatchSeq   int
	// PendingUserInput blocks dispatch while human input is pending.
	PendingUserInput bool
	// WrapupGatesLoaded confirms the synthesis gates were computed.
	WrapupGatesLoaded bool
	// BatchReadyForSynthesis is true when every synthesis gate holds.
	BatchReadyForSynthesis bool
	// OpenRepairSinceUserIntent is true when a failed or partial worker leg needs repair.
	OpenRepairSinceUserIntent bool
	// VerifyUnverified marks an exhausted verification budget without a pass.
	VerifyUnverified bool
	// VerifyCommand is the project's declared test command.
	VerifyCommand string
	// ProgressOpenCount counts open blocking checklist rows.
	ProgressOpenCount int
	// ProgressMissing is true when the root session has no checklist rows yet.
	ProgressMissing bool
	// ProgressGatedToolAttemptedSinceIntent includes rejected attempts.
	ProgressGatedToolAttemptedSinceIntent bool
}

// WithWrapupGates records computed synthesis gates.
func WithWrapupGates(state ImplementSessionState, batchReady, openRepair bool) ImplementSessionState {
	state.WrapupGatesLoaded = true
	state.BatchReadyForSynthesis = batchReady
	state.OpenRepairSinceUserIntent = openRepair
	return state
}

// HostLoopWakeTurn reports an explicitly tagged host loop re-entry.
func HostLoopWakeTurn(history []api.Message) bool {
	for _, msg := range history[api.UserIntentBoundary(history):] {
		if msg.Role == api.MessageRoleUser && msg.Kind == api.MessageKindHostLoopWake {
			return true
		}
	}
	return false
}

// WorkerTaskFinishedTurn reports the catalog anchor on the current host turn.
func WorkerTaskFinishedTurn(history []api.Message) bool {
	for _, msg := range history[api.UserIntentBoundary(history):] {
		if msg.Role == api.MessageRoleUser && msg.Kind == api.MessageKindHostKick && msg.HostSignalID == anchor.WorkerTaskFinished.String() {
			return true
		}
	}
	return false
}

// HostCycleTurn reports loop-wake or worker-task-finished host turns.
func HostCycleTurn(history []api.Message) bool {
	return HostLoopWakeTurn(history) || WorkerTaskFinishedTurn(history)
}

// TerminalWorkerCompletionMessage reports assistant envelopes for complete or partial workers.
func TerminalWorkerCompletionMessage(msg api.Message) bool {
	if msg.WorkerSummary == nil {
		return false
	}
	return msg.WorkerSummary.Status == api.WorkerSummaryStatusComplete ||
		msg.WorkerSummary.Status == api.WorkerSummaryStatusPartial
}

// LatestTerminalWorkerAgentType returns agent_type from the newest terminal worker envelope.
func LatestTerminalWorkerAgentType(history []api.Message) string {
	for i := len(history) - 1; i >= 0; i-- {
		if !TerminalWorkerCompletionMessage(history[i]) {
			continue
		}
		if agentType := strings.TrimSpace(history[i].WorkerSummary.AgentType); agentType != "" {
			return agentType
		}
		return ""
	}
	return ""
}

// TerminalWorkerCompletionSince finds a terminal worker envelope after sinceIdx.
func TerminalWorkerCompletionSince(history []api.Message, sinceIdx int) bool {
	for i := sinceIdx; i < len(history); i++ {
		if TerminalWorkerCompletionMessage(history[i]) {
			return true
		}
	}
	return false
}

// PartialWorkerSummaryJobIDs returns job ids whose latest structured summary is partial.
func PartialWorkerSummaryJobIDs(history []api.Message) []string {
	var ids []string
	seen := map[string]struct{}{}
	for i := len(history) - 1; i >= 0; i-- {
		meta := history[i].WorkerSummary
		if meta == nil || meta.Status != api.WorkerSummaryStatusPartial {
			continue
		}
		id := strings.TrimSpace(meta.WorkerID)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids
}

// PendingOverlaySummaryIDs returns job ids whose latest WorkerSummary is still open.
func PendingOverlaySummaryIDs(history []api.Message) []string {
	var ids []string
	seen := map[string]struct{}{}
	for i := len(history) - 1; i >= 0; i-- {
		meta := history[i].WorkerSummary
		if meta == nil || meta.Status != api.WorkerSummaryStatusOpen {
			continue
		}
		id := strings.TrimSpace(meta.WorkerID)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids
}
