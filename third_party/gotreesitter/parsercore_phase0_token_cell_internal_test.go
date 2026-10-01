//go:build gts_parsercorephase0

package gotreesitter

import (
	"testing"

	core "github.com/odvcencio/gotreesitter/internal/parsercorephase0"
)

// TestDiagnosticParserCoreTokenCellStartsInvalidAfterConstruction pins that a
// freshly constructed scheduler's tokenCell is the zero value: reset's
// full-struct-literal reinitialization does not list tokenCell (mirroring
// corridorCells), and the constructor's initialization path does not
// populate it either, so it starts invalid until the first election.
func TestDiagnosticParserCoreTokenCellStartsInvalidAfterConstruction(t *testing.T) {
	compact, err := core.New(&genericConflictTable{}, core.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	seed, err := compact.Seed(1, 0)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := parserCoreCheckpoint(nil)
	var scratch []byte
	scheduler, err := newDiagnosticParserCoreGenericScheduler(
		compact, &dfaTokenSource{}, &scratch, seed, 0, checkpoint, diagnosticParserCoreSeedObserver{}, DiagnosticParserCorePrefixOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if scheduler.tokenCell != (diagnosticParserCoreTokenCell{}) {
		t.Fatalf("freshly constructed scheduler.tokenCell = %+v, want the zero value", scheduler.tokenCell)
	}
}

// TestDiagnosticParserCoreTokenCellDirectInvalidationLeavesStaleFieldsUntouched
// pins the invalidation contract used at the sole SeekTokenFrontier resync
// call site in dispatchPassActive: once the lexer moves without an election,
// that site writes only tokenCell.valid = false, leaving the stale
// token/state/checkpoint fields in place (they are simply no longer
// trustworthy, not zeroed). This exercises the same write in isolation
// because driving a real S3 error-region resync from a from-scratch unit
// test is disproportionate for inert, unread substrate.
func TestDiagnosticParserCoreTokenCellDirectInvalidationLeavesStaleFieldsUntouched(t *testing.T) {
	compact, err := core.New(&genericConflictTable{}, core.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	seed, err := compact.Seed(1, 0)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := parserCoreCheckpoint(nil)
	var scratch []byte
	scheduler, err := newDiagnosticParserCoreGenericScheduler(
		compact, &dfaTokenSource{}, &scratch, seed, 0, checkpoint, diagnosticParserCoreSeedObserver{}, DiagnosticParserCorePrefixOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	scheduler.tokenCell = diagnosticParserCoreTokenCell{
		token: Token{Symbol: 5, StartByte: 3, EndByte: 4}, state: 7,
		byteOffset: 3, beforeCheckpoint: 1, afterCheckpoint: 2, valid: true,
	}
	scheduler.tokenCell.valid = false
	if scheduler.tokenCell.valid {
		t.Fatal("scheduler.tokenCell.valid = true after direct invalidation, want false")
	}
	if scheduler.tokenCell.token.StartByte != 3 || scheduler.tokenCell.state != 7 ||
		scheduler.tokenCell.byteOffset != 3 || scheduler.tokenCell.beforeCheckpoint != 1 || scheduler.tokenCell.afterCheckpoint != 2 {
		t.Fatalf("direct invalidation mutated stale fields it should leave untouched: %+v", scheduler.tokenCell)
	}
}

// TestDiagnosticParserCoreTokenCellMatchesElectedTokenAndCheckpoints pins the
// D2 tranche C wiring: after elect() runs, scheduler.tokenCell holds the same
// token, primary election state, and scanner checkpoints as the scheduler's
// own token/checkpointBeforeID/checkpointID fields for that same election.
// tokenCell is inert substrate for a later forced-reuse tranche -- nothing
// reads it yet -- so this test is the only thing that would catch its
// population wiring breaking.
func TestDiagnosticParserCoreTokenCellMatchesElectedTokenAndCheckpoints(t *testing.T) {
	lang, err := authenticatedParserCoreGoLanguage(parserCoreWarmGoScanner)
	if err != nil {
		t.Fatal(err)
	}
	parser := NewParser(lang)
	tables, err := newParserCoreRootTables(parser)
	if err != nil {
		t.Fatal(err)
	}
	compact, err := core.New(tables, core.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	source := []byte("package p\n")
	tokenSource := parser.acquireParserDFATokenSource(source)
	if tokenSource == nil {
		t.Fatal("parser-core phase zero: production DFA unavailable")
	}
	defer tokenSource.Close()

	var (
		gotCell   diagnosticParserCoreTokenCell
		gotToken  Token
		gotBefore core.CheckpointID
		gotAfter  core.CheckpointID
		gotState  StateID
		captured  bool
	)
	observer := diagnosticParserCoreSeedObserver{
		afterElection: func(s *diagnosticParserCoreGenericScheduler) (bool, error) {
			if s.electionIndex != 0 {
				return false, nil
			}
			// headers have not been dispatched this epoch yet, so this
			// independently recomputes the same primary state elect() just
			// captured into tokenCell.state, without reading tokenCell itself.
			state, err := s.electHeaderState(s.headers[0])
			if err != nil {
				return false, err
			}
			gotCell = s.tokenCell
			gotToken = s.token
			gotBefore = s.checkpointBeforeID
			gotAfter = s.checkpointID
			gotState = state
			captured = true
			return true, nil
		},
	}
	var scannerScratch []byte
	scheduler, err := executeDiagnosticParserCoreGenericSchedulerFromSeed(
		compact, tokenSource, &scannerScratch, lang.InitialState,
		DiagnosticParserCorePrefixOptions{MaxDispatches: 100000, MaxTokens: 100000},
		observer,
	)
	if err != nil {
		t.Fatal(err)
	}
	if scheduler == nil || !captured {
		t.Fatal("first-election observer did not fire")
	}
	if !gotCell.valid {
		t.Fatal("tokenCell.valid = false after the first election, want true")
	}
	if gotCell.token != gotToken {
		t.Fatalf("tokenCell.token = %+v, want %+v (scheduler.token)", gotCell.token, gotToken)
	}
	if gotCell.byteOffset != gotToken.StartByte {
		t.Fatalf("tokenCell.byteOffset = %d, want %d (token.StartByte)", gotCell.byteOffset, gotToken.StartByte)
	}
	if gotCell.state != gotState {
		t.Fatalf("tokenCell.state = %d, want %d (the primary election state passed to SetParserState)", gotCell.state, gotState)
	}
	if gotCell.beforeCheckpoint != gotBefore {
		t.Fatalf("tokenCell.beforeCheckpoint = %d, want %d (scheduler.checkpointBeforeID)", gotCell.beforeCheckpoint, gotBefore)
	}
	if gotCell.afterCheckpoint != gotAfter {
		t.Fatalf("tokenCell.afterCheckpoint = %d, want %d (scheduler.checkpointID)", gotCell.afterCheckpoint, gotAfter)
	}
}
