package guard

import (
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/batch"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/pkg/api"
)

const (
	CoordinatorBatchAlreadyClosedCode = "COORDINATOR_BATCH_ALREADY_CLOSED"
	CoordinatorBatchWrongPhaseCode    = "COORDINATOR_BATCH_WRONG_PHASE"
)

// BatchTurnGuard carries synchronous in-turn host state for batch lifecycle guards.
type BatchTurnGuard struct {
	SynthesisAcceptedThisTurn bool
}

func batchPhaseClosed(phase string) bool {
	return strings.TrimSpace(strings.ToLower(phase)) == batch.PhaseClosed
}

func batchAlreadyClosed(latch BatchTurnGuard, phase string) bool {
	return latch.SynthesisAcceptedThisTurn || batchPhaseClosed(phase)
}

// ObserveCoordinatorBatchPhaseTool publishes batch-phase tool facts.
func ObserveCoordinatorBatchPhaseTool(
	sess *api.Session,
	toolName string,
	implState surface.ImplementSessionState,
	latch BatchTurnGuard,
	gc *oar.GuardContext,
) {
	if gc == nil || sess == nil || sess.IsWorkerChild() {
		return
	}
	toolName = strings.TrimSpace(strings.ToLower(toolName))
	if toolName == "" {
		return
	}
	gc.Tool = toolName
	gc.DeriveToolClassFacts()
	gc.BatchPhase = implState.BatchPhase
	gc.BatchClosed = batchAlreadyClosed(latch, implState.BatchPhase)
}
