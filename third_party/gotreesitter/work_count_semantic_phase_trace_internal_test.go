//go:build gts_workcount

package gotreesitter

import (
	"strings"
	"testing"
)

func TestSemanticPhaseExactScannerCheckpointNeverCoercesUnavailableToEmpty(t *testing.T) {
	previousConvergence := activeWorkCountConvergence
	t.Cleanup(func() { activeWorkCountConvergence = previousConvergence })
	parser := &Parser{language: &Language{ExternalScanner: &mockExternalScanner{}}}
	tok := Token{Symbol: 7, StartByte: 11, EndByte: 14, ExternalScannerToken: true}
	missing := semanticPhaseScannerCheckpoint(parser, tok)
	if missing.State != "unavailable" || missing.Reason != "current_token_checkpoint_not_exposed" || missing.SHA256 != "" {
		t.Fatalf("missing checkpoint was not fail-closed: %+v", missing)
	}
	parser.currentExternalTokenCheckpointValid = true
	parser.currentExternalTokenCheckpoint = externalScannerCheckpoint{start: []byte{1, 2}, end: []byte{3, 4, 5}}
	parser.currentExternalTokenCheckpointStart = tok.StartByte
	parser.currentExternalTokenCheckpointEnd = tok.EndByte
	exact := semanticPhaseScannerCheckpoint(parser, tok)
	if exact.State != "exact" || exact.StartByte != tok.StartByte || exact.EndByte != tok.EndByte || exact.StartLength != 2 || exact.EndLength != 3 || len(exact.SHA256) != 64 || strings.Trim(exact.SHA256, "0123456789abcdef") != "" {
		t.Fatalf("exact checkpoint identity invalid: %+v", exact)
	}
	if exact.SHA256 != semanticPhaseCheckpointDigest(tok.StartByte, tok.EndByte, []byte{1, 2}, []byte{3, 4, 5}) || exact.SHA256 == semanticPhaseCheckpointDigest(tok.StartByte, tok.EndByte, nil, nil) {
		t.Fatal("exact checkpoint digest lost byte/length identity")
	}
	relexed := Token{Symbol: 9, StartByte: tok.StartByte, EndByte: tok.EndByte + 1, ExternalScannerToken: true}
	workCountSetConvergenceLookahead(tok)
	workCountRefreshConvergenceLookahead(relexed)
	mismatch := semanticPhaseScannerCheckpoint(parser, activeWorkCountConvergence.lookahead)
	if mismatch.State != "unavailable" || mismatch.Reason != "current_token_checkpoint_span_mismatch" || mismatch.SHA256 != "" {
		t.Fatalf("relexed token inherited stale checkpoint: %+v", mismatch)
	}
	parser.currentExternalTokenCheckpointStart = relexed.StartByte
	parser.currentExternalTokenCheckpointEnd = relexed.EndByte
	refreshed := semanticPhaseScannerCheckpoint(parser, activeWorkCountConvergence.lookahead)
	if refreshed.State != "exact" || refreshed.StartByte != relexed.StartByte || refreshed.EndByte != relexed.EndByte || refreshed.SHA256 == exact.SHA256 {
		t.Fatalf("relexed checkpoint was not rebound to the current span: old=%+v new=%+v", exact, refreshed)
	}
}

func TestSemanticPhaseCollisionAuditFailsClosed(t *testing.T) {
	previous := activeDiagnosticSemanticPhaseTrace
	t.Cleanup(func() { activeDiagnosticSemanticPhaseTrace = previous })
	activeDiagnosticSemanticPhaseTrace = nil
	BeginDiagnosticSemanticPhaseTrace()
	semanticPhaseAuditActionCell([]ParseAction{{Type: ParseActionShift, State: 1}}, 7)
	semanticPhaseAuditActionCell([]ParseAction{{Type: ParseActionReduce, Symbol: 2}}, 7)
	first := newGLRStack(1)
	second := newGLRStack(2)
	semanticPhaseAuditBoundary(&first, 9)
	semanticPhaseAuditBoundary(&second, 9)
	trace := EndDiagnosticSemanticPhaseTrace()
	if trace.CollisionAudit.Complete || trace.CollisionAudit.ActionCellHashCollisions != 1 || trace.CollisionAudit.CoarseBoundaryHashCollisions != 1 {
		t.Fatalf("collision audit did not fail closed: %+v", trace.CollisionAudit)
	}
}

func TestSemanticPhaseCollisionAuditCapacityFailsClosed(t *testing.T) {
	previous := activeDiagnosticSemanticPhaseTrace
	t.Cleanup(func() { activeDiagnosticSemanticPhaseTrace = previous })
	activeDiagnosticSemanticPhaseTrace = nil
	BeginDiagnosticSemanticPhaseTrace()
	activeDiagnosticSemanticPhaseTrace.CollisionAudit.MaxActionCellDistinctHashes = 1
	activeDiagnosticSemanticPhaseTrace.CollisionAudit.MaxCoarseBoundaryDistinctHashes = 1
	semanticPhaseAuditActionCell([]ParseAction{{Type: ParseActionShift, State: 1}}, 7)
	semanticPhaseAuditActionCell([]ParseAction{{Type: ParseActionReduce, Symbol: 2}}, 8)
	first := newGLRStack(1)
	second := newGLRStack(2)
	semanticPhaseAuditBoundary(&first, 9)
	semanticPhaseAuditBoundary(&second, 10)
	trace := EndDiagnosticSemanticPhaseTrace()
	if trace.CollisionAudit.Complete || !trace.CollisionAudit.ActionCellAuditTruncated || !trace.CollisionAudit.CoarseBoundaryAuditTruncated ||
		trace.CollisionAudit.ActionCellKeysUnaudited != 1 || trace.CollisionAudit.CoarseBoundaryKeysUnaudited != 1 ||
		trace.CollisionAudit.ActionCellDistinctHashes != 1 || trace.CollisionAudit.CoarseBoundaryDistinctHashes != 1 {
		t.Fatalf("bounded collision audit did not fail closed: %+v", trace.CollisionAudit)
	}
}

func TestSemanticPhaseAggregatesSurviveRetentionTruncation(t *testing.T) {
	previous := activeDiagnosticSemanticPhaseTrace
	t.Cleanup(func() { activeDiagnosticSemanticPhaseTrace = previous })
	activeDiagnosticSemanticPhaseTrace = nil
	BeginDiagnosticSemanticPhaseTrace()
	activeDiagnosticSemanticPhaseTrace.MaxEvents = 1
	semanticPhaseTraceAppend(DiagnosticSemanticPhaseEvent{Kind: "action_lookup", Outcome: "candidate"})
	semanticPhaseTraceAppend(DiagnosticSemanticPhaseEvent{Kind: "action_execution", ActionType: int16(ParseActionShift), Phase: "shift"})
	semanticPhaseTraceAppend(DiagnosticSemanticPhaseEvent{Kind: "decision", Phase: "merge", Outcome: "packed"})
	trace := EndDiagnosticSemanticPhaseTrace()
	if trace.EventsSeen != 3 || len(trace.Events) != 1 || trace.EventsDropped != 2 || trace.Aggregates.ActionLookupEvents != 1 || trace.Aggregates.ActionExecutionEvents != 1 || trace.Aggregates.DecisionEvents != 1 {
		t.Fatalf("truncated aggregate receipt drifted: %+v", trace)
	}
}

func TestWorkCountRefreshConvergenceLookaheadKeepsElectionOrdinal(t *testing.T) {
	previous := activeWorkCountConvergence
	t.Cleanup(func() { activeWorkCountConvergence = previous })
	activeWorkCountConvergence.electionOrdinal = 0
	activeWorkCountConvergence.lookahead = Token{}
	activeWorkCountConvergence.lookaheadPresent = false

	initial := Token{Symbol: 7, StartByte: 11, EndByte: 14}
	relexed := Token{Symbol: 9, StartByte: 11, EndByte: 15}
	workCountSetConvergenceLookahead(initial)
	ordinal := activeWorkCountConvergence.electionOrdinal
	workCountRefreshConvergenceLookahead(relexed)

	if ordinal != 1 || activeWorkCountConvergence.electionOrdinal != ordinal {
		t.Fatalf("refresh changed election ordinal: before=%d after=%d", ordinal, activeWorkCountConvergence.electionOrdinal)
	}
	if activeWorkCountConvergence.lookahead != relexed || !activeWorkCountConvergence.lookaheadPresent {
		t.Fatalf("refresh did not publish current lookahead: got=%+v present=%v want=%+v", activeWorkCountConvergence.lookahead, activeWorkCountConvergence.lookaheadPresent, relexed)
	}
}

func TestSemanticPhaseTraceEOFPrefixTableRoute(t *testing.T) {
	previousConvergence := activeWorkCountConvergence
	previousTrace := activeDiagnosticSemanticPhaseTrace
	t.Cleanup(func() {
		activeWorkCountConvergence = previousConvergence
		activeDiagnosticSemanticPhaseTrace = previousTrace
	})
	activeWorkCountConvergence.electionOrdinal = 0
	activeWorkCountConvergence.iteration = 17
	activeWorkCountConvergence.lookahead = Token{}
	activeWorkCountConvergence.lookaheadPresent = false
	activeDiagnosticSemanticPhaseTrace = nil

	lang := &Language{
		Name:            "semantic-trace-eof-prefix",
		SymbolCount:     3,
		TokenCount:      1,
		StateCount:      3,
		LargeStateCount: 3,
		InitialState:    1,
		SymbolNames:     []string{"end", "unused", "root"},
		SymbolMetadata: []SymbolMetadata{
			{Name: "end"},
			{Name: "unused"},
			{Name: "root", Visible: true, Named: true},
		},
		ParseTable: [][]uint16{
			{0, 0, 0},
			{1, 0, 2}, // EOF reduces root; root GOTO enters state 2.
			{2, 0, 0}, // EOF then accepts.
		},
		ParseActions: []ParseActionEntry{
			{},
			{Actions: []ParseAction{{Type: ParseActionReduce, Symbol: 2}}},
			{Actions: []ParseAction{{Type: ParseActionAccept}}},
		},
	}
	parser := NewParser(lang)
	stack := newGLRStack(1)
	tok := Token{}
	workCountSetConvergenceLookahead(tok)
	BeginDiagnosticSemanticPhaseTrace()

	arena := newNodeArena(arenaClassFull)
	var entryScratch glrEntryScratch
	var gssScratch gssScratch
	var tmpEntries []stackEntry
	nodeCount := 0
	insertedMissing, advanced := parser.advanceTrailingEOFPrefix(&stack, tok, 0, nil, &nodeCount, arena, &entryScratch, &gssScratch, &tmpEntries)
	trace := EndDiagnosticSemanticPhaseTrace()

	if insertedMissing || !advanced || !stack.accepted {
		t.Fatalf("EOF prefix route inserted_missing=%v advanced=%v accepted=%v", insertedMissing, advanced, stack.accepted)
	}
	var lookups, executions int
	wantPhases := map[string]int{"eof-prefix-reduce": 1, "eof-prefix-accept": 1}
	for _, event := range trace.Events {
		switch event.Kind {
		case "action_lookup":
			lookups++
			if event.ActionOrdinal != 0 || event.TokenOrdinal != 1 || event.Iteration != 17 {
				t.Fatalf("EOF lookup context=%+v", event)
			}
		case "action_execution":
			executions++
			wantPhases[event.Phase]--
			if event.ActionOrdinal != 0 || event.TokenOrdinal != 1 || event.Iteration != 17 {
				t.Fatalf("EOF execution context=%+v", event)
			}
		}
	}
	if lookups != 2 || executions != 2 || wantPhases["eof-prefix-reduce"] != 0 || wantPhases["eof-prefix-accept"] != 0 {
		t.Fatalf("EOF route lookups=%d executions=%d phases=%v", lookups, executions, wantPhases)
	}
}
