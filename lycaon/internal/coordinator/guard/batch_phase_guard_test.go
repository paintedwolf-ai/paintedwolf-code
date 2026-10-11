package guard_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/batch"
	"github.com/lycaon/lycaon/internal/coordinator/guard"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestObserveHostNoToolTurn_batchAlreadyClosedInTurnLatch(t *testing.T) {
	fmt := coordinatorRejectFmt(t)
	reject, block := guard.FormatHostNoToolTurnReject(
		&api.Session{ID: "s1", AgentType: "coordinator"}, hostLoopHistory(

			nil),

		"Second synthesis attempt.",
		nil,
		"implement_synthesis",
		true,
		surface.ImplementSessionState{BatchPhase: batch.PhaseSynthesize},
		fmt,
		guard.BatchTurnGuard{SynthesisAcceptedThisTurn: true})

	if !block || !strings.Contains(reject, guard.CoordinatorBatchAlreadyClosedCode) {
		t.Fatalf("reject=%q block=%v", reject, block)
	}
}

func TestObserveHostNoToolTurn_batchAlreadyClosedPhase(t *testing.T) {
	fmt := coordinatorRejectFmt(t)
	reject, block := guard.FormatHostNoToolTurnReject(
		&api.Session{ID: "s1", AgentType: "coordinator"}, hostLoopHistory(

			nil),

		"Late host prose.",
		nil,
		"implement_synthesis",
		true,
		surface.ImplementSessionState{BatchPhase: batch.PhaseClosed},
		fmt,
		guard.BatchTurnGuard{})

	if !block || !strings.Contains(reject, guard.CoordinatorBatchAlreadyClosedCode) {
		t.Fatalf("reject=%q block=%v", reject, block)
	}
}

func TestObserveHostNoToolTurn_allowsFirstSynthesis(t *testing.T) {
	fmt := coordinatorRejectFmt(t)
	reject, block := guard.FormatHostNoToolTurnReject(
		&api.Session{ID: "s1", AgentType: "coordinator"}, hostLoopHistory(

			nil),

		coordinatorReportJSON(t, "Summary for the user.", []guidance.CoordinatorCitedEvidence{{
			Path: "internal/retention.go", Line: 1, Excerpt: "package retention",
		}}),
		nil,
		"implement_synthesis",
		true,
		surface.ImplementSessionState{BatchPhase: batch.PhaseSynthesize},
		fmt,
		guard.BatchTurnGuard{})

	if block {
		t.Fatalf("reject=%q block=%v want allow", reject, block)
	}
}

func TestObserveCoordinatorBatchPhaseTool_synthesizePackBoard(t *testing.T) {
	sess := &api.Session{Posture: api.SessionPostureBuild}
	gc := oar.NewGuardContext()
	guard.ObserveCoordinatorBatchPhaseTool(
		sess,
		"pack_board",
		surface.ImplementSessionState{BatchPhase: batch.PhaseSynthesize},
		guard.BatchTurnGuard{},
		gc,
	)
	if !observeHasCode(gc, guard.CoordinatorBatchWrongPhaseCode) {
		t.Fatalf("want %s in %v", guard.CoordinatorBatchWrongPhaseCode, gc.Invocation.ArgValidationErrors)
	}
}

func TestObserveCoordinatorBatchPhaseTool_closedPackBoard(t *testing.T) {
	sess := &api.Session{Posture: api.SessionPostureBuild}
	gc := oar.NewGuardContext()
	guard.ObserveCoordinatorBatchPhaseTool(
		sess,
		"pack_board",
		surface.ImplementSessionState{BatchPhase: batch.PhaseClosed},
		guard.BatchTurnGuard{},
		gc,
	)
	if !observeHasCode(gc, guard.CoordinatorBatchAlreadyClosedCode) {
		t.Fatalf("want %s in %v", guard.CoordinatorBatchAlreadyClosedCode, gc.Invocation.ArgValidationErrors)
	}
}

func TestObserveCoordinatorBatchPhaseTool_latchBlocksPackBoard(t *testing.T) {
	sess := &api.Session{Posture: api.SessionPostureBuild}
	gc := oar.NewGuardContext()
	guard.ObserveCoordinatorBatchPhaseTool(
		sess,
		"pack_board",
		surface.ImplementSessionState{BatchPhase: batch.PhaseSynthesize},
		guard.BatchTurnGuard{SynthesisAcceptedThisTurn: true},
		gc,
	)
	if !observeHasCode(gc, guard.CoordinatorBatchAlreadyClosedCode) {
		t.Fatalf("want %s in %v", guard.CoordinatorBatchAlreadyClosedCode, gc.Invocation.ArgValidationErrors)
	}
}

func TestFormatHostNoToolTurnReject_secondProseAfterLatchWithPriorTools(t *testing.T) {
	fmt := coordinatorRejectFmt(t)
	sess := &api.Session{Posture: api.SessionPostureBuild, WorkspacePath: t.TempDir()}
	history := groundedLegHistory("implementer")
	prose := coordinatorReportJSON(t, "Summary for the user.", []guidance.CoordinatorCitedEvidence{{
		Path: "internal/retention.go", Line: 1, Excerpt: "package retention",
	}})
	reject, block := guard.FormatHostNoToolTurnReject(
		sess, hostLoopHistory(

			history),

		prose,
		[]string{"pack_board"},
		"implement_synthesis",
		true,
		surface.ImplementSessionState{BatchPhase: batch.PhaseSynthesize},
		fmt,
		guard.BatchTurnGuard{SynthesisAcceptedThisTurn: true})

	if !block || !strings.Contains(reject, guard.CoordinatorBatchAlreadyClosedCode) {
		t.Fatalf("reject=%q block=%v want second prose blocked by latch even with turnTools", reject, block)
	}
}
