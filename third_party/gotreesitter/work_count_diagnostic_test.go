//go:build gts_workcount

package gotreesitter

import (
	"math"
	"testing"
)

func TestDiagnosticWorkCountSaturatesAndFlagsOverflow(t *testing.T) {
	BeginDiagnosticWorkCount()
	activeDiagnosticWorkCount.Shifts = math.MaxUint64
	workCountRecordShift()
	counts := EndDiagnosticWorkCount()
	if !counts.Overflow {
		t.Fatal("overflow flag is false")
	}
	if counts.Shifts != math.MaxUint64 {
		t.Fatalf("shifts=%d want saturation at %d", counts.Shifts, uint64(math.MaxUint64))
	}
}

func TestDiagnosticWorkCountLinearReductionPop(t *testing.T) {
	leaf := newLeafNodeInArena(nil, 1, true, 0, 1, Point{}, Point{Column: 1})
	extra := newLeafNodeInArena(nil, 2, false, 1, 2, Point{Column: 1}, Point{Column: 2})
	extra.setExtra(true)
	stack := newGLRStack(1)
	stack.entries = append(stack.entries,
		newStackEntryNode(2, leaf),
		newStackEntryNode(2, extra),
	)

	BeginDiagnosticWorkCount()
	workCountObserveReductionPop(&stack, 1)
	counts := EndDiagnosticWorkCount()
	if counts.EmittedPopPaths != 1 || counts.EmittedPopPayloads != 2 {
		t.Fatalf("paths=%d payloads=%d want 1/2", counts.EmittedPopPaths, counts.EmittedPopPayloads)
	}
}

func TestDiagnosticWorkCountBoardDirectEvents(t *testing.T) {
	BeginDiagnosticWorkCount()
	workCountRecordFrontierLexerElection()
	workCountRecordRawMainLexerInvocation()
	workCountRecordRawMainLexerInvocation()
	workCountRecordResolvedActionCell(4)
	workCountRecordResolvedActionCell(1)
	workCountRecordAlternatePredecessorLinkAppended()
	counts := EndDiagnosticWorkCount().BoardDirect()
	if counts.Schema != "gts-work-count-board-direct/v3" || counts.Overflow || !counts.FrontierLexerElectionsAvailable || counts.FrontierLexerElections != 1 || counts.PerVersionLexRequestsAvailable || counts.PerVersionLexRequests != 0 || counts.RawMainLexerInvocations != 2 || counts.ResolvedActionCellsExamined != 2 || counts.RawActionEntriesBeyondFirst != 3 || counts.AlternatePredecessorLinksAppended != 1 {
		t.Fatalf("board direct counts=%+v", counts)
	}
}

func TestDiagnosticWorkCountSelectedCensusSaturates(t *testing.T) {
	counts := DiagnosticWorkCount{
		DiagnosticWorkCountValues: DiagnosticWorkCountValues{SelectedNodes: math.MaxUint64},
	}
	counts.AddDiagnosticSelectedNode(false)
	if !counts.Overflow || counts.SelectedNodes != math.MaxUint64 || counts.SelectedLeafNodes != 1 {
		t.Fatalf("selected census saturation=%+v", counts)
	}
}

func TestDiagnosticRetryTraceRecordsAttemptsWithoutWorkObserver(t *testing.T) {
	BeginDiagnosticRetryTrace()
	if activeDiagnosticWorkCount != nil {
		t.Fatal("attempt-only trace enabled the work-count ledger")
	}
	if workCountConvergenceActive() {
		t.Fatal("attempt-only trace enabled the convergence observer")
	}
	workCountRecordShift()
	workCountSetNextParseAttempt("initial_full", "fresh_dfa_full_parse")
	token := workCountBeginParseAttempt(8, 100, 6)
	workCountResolveParseAttempt(token, 8, false, 6, 10, 1000, 100)
	workCountBeginFinalizeParseAttempt(token)
	workCountEndFinalizeParseAttempt(token, ParseStopNoStacksAlive, nil)
	trace := EndDiagnosticRetryTrace()

	if len(trace.Attempts) != 1 {
		t.Fatalf("attempts=%d want=1", len(trace.Attempts))
	}
	attempt := trace.Attempts[0]
	if attempt.LogicalRung != "initial_full" || attempt.OperationCause != "fresh_dfa_full_parse" ||
		attempt.StopReason != ParseStopNoStacksAlive || attempt.ResolvedMaxStacks != 8 ||
		attempt.ResolvedRetryPass || attempt.ResolvedMaxMergePerKey != 6 {
		t.Fatalf("attempt=%+v", attempt)
	}
	if attempt.Counters.Shifts != 0 {
		t.Fatalf("attempt shifts=%d want=0", attempt.Counters.Shifts)
	}
}

func TestDiagnosticWorkCountStillRecordsAttemptBoundaries(t *testing.T) {
	BeginDiagnosticWorkCount()
	workCountSetNextParseAttempt("initial_full", "fresh_dfa_full_parse")
	token := workCountBeginParseAttempt(8, 100, 6)
	workCountResolveParseAttempt(token, 8, false, 6, 10, 1000, 100)
	workCountBeginFinalizeParseAttempt(token)
	workCountEndFinalizeParseAttempt(token, ParseStopAccepted, nil)
	counts := EndDiagnosticWorkCount()

	if len(counts.Attempts) != 1 {
		t.Fatalf("attempts=%d want=1", len(counts.Attempts))
	}
	if attempt := counts.Attempts[0]; attempt.StopReason != ParseStopAccepted || attempt.ResolvedMaxStacks != 8 {
		t.Fatalf("attempt=%+v", attempt)
	}
}
