package parsercorephase0

import "testing"

// This file targets condenseWithOutcomeAtomic's three shallow-fold early
// returns (structural match, precedence-drop, precedence-replace, all in
// core.go's foldSamePredecessorShallowPayloads block) and the buildOutcome
// helper that now backs every return in that function, including the
// duplicate-drop return above it and the freshly-published-node return below
// it. Before this change, only the duplicate-drop and freshly-published
// returns copied the boundary's historical provenance snapshot
// (historicalBoundarySplit / historicalConvergedSplit /
// historicalForestDeterministic / historicalCleanPathRank /
// historicalLineage) into the returned condenseOutcome; the three fold
// returns discarded it.
//
// historicalFoldFixture below reaches condenseWithOutcomeAtomic through the
// same live-condensation setup the historical-boundary tests in
// core_test.go use (TestHistoricalBoundaryProvesDeterministicForest et al.):
// it records real reduction lineage on a pre-scope incumbent, then folds a
// historical-split replacement for it inside runLiveCondenseCandidates. That
// replacement call resolves through the freshly-published-node return
// (oldID is reset to 0 by the historical-split branch), so it is the one
// carrying genuine non-zero provenance in this fixture.
//
// The fold call under test then targets that live survivor. Reaching the
// live survivor's own links makes condenseWithOutcomeAtomic's boundary probe
// see it as live, so *that* call's own historicalBoundarySplit computation
// is false by construction -- a live incumbent and a historical predecessor
// are mutually exclusive within one condenseWithOutcomeAtomic invocation,
// because the historical branch unconditionally zeroes oldID before the
// duplicate/shallow-fold logic ever runs (core.go, condenseWithOutcomeAtomic).
// So the fold call's own outcome is expected to carry zero-value historical
// fields here: the assertions below confirm buildOutcome propagates the
// caller's actual (here: empty) snapshot rather than fabricating the nearby
// but unrelated historical signal from the fixture's setup call. That is the
// "merge, never invent" contract buildOutcome exists to enforce uniformly.
//
// If a future change ever lets a live incumbent and a historical
// predecessor coexist in one call (for example, by making the historical
// reset conditional), these three returns going through buildOutcome is
// what makes that newly-reachable provenance propagate correctly instead of
// silently reproducing the bug this change fixes.
func historicalFoldFixture(t *testing.T, foldOnSurvivor func(core *Core, key boundaryKey, prev NodeID) (condenseOutcome, error)) (setup, fold condenseOutcome) {
	t.Helper()
	core, seed := newDiagnosticShallowFoldCore(t, Limits{MaxDerivations: 8, MaxLinksPerBoundary: 4})
	incumbent := appendShallowPayload(t, core, shallowPayloadSpec{
		symbol: 20, productionID: 1, startByte: 12, endByte: 17, childSymbols: []Symbol{30},
	})
	key := core.boundaryKey(2, 17)
	input := linkInput{prev: seed.Node, payload: incumbent, scoreDelta: 5, order: ForkOrder{Present: true, Value: 1}}
	err := core.ApplySchedulerAtomic(func(owner SchedulerTransactionToken) error {
		preScope, err := core.condense(key, input)
		if err != nil {
			return err
		}
		if err := core.RecordReductionLineageOwned(owner, []ReductionOutput{{
			Head: preScope, CleanPathRank: CleanPathRankSelected, MultiplePopPaths: true,
		}}, 7); err != nil {
			return err
		}
		return core.runLiveCondenseCandidates(nil, func() error {
			setup, err = core.condenseWithOutcome(key, input)
			if err != nil {
				return err
			}
			// The fold call under test must share the survivor's own link
			// predecessor (seed.Node), not the survivor node itself: prev is
			// the edge target for the new link, and shallowPayloadClassEqual
			// / linkEqualInput require it to match the incumbent link's prev
			// for the shallow-fold search to consider it at all.
			fold, err = foldOnSurvivor(core, key, seed.Node)
			return err
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	// Confirm the fixture actually captured non-trivial historical
	// provenance, so the fold call's zero-value assertions below are proving
	// something (the fold did not merely inherit an already-empty
	// snapshot).
	if !setup.historicalBoundarySplit || !setup.historicalConvergedSplit ||
		setup.historicalCleanPathRank != CleanPathRankSelected || setup.historicalLineage != 7 {
		t.Fatalf("fixture did not establish historical provenance on the survivor: setup=%+v", setup)
	}
	return setup, fold
}

func assertNoFabricatedHistory(t *testing.T, outcome condenseOutcome) {
	t.Helper()
	if outcome.historicalBoundarySplit || outcome.historicalConvergedSplit || outcome.historicalForestDeterministic ||
		outcome.historicalCleanPathRank != CleanPathRankNotApplicable || outcome.historicalLineage != 0 {
		t.Fatalf("fold outcome invented historical provenance it was never given: %+v", outcome)
	}
}

func TestFoldStructuralMatchPropagatesProvenanceWithoutFabrication(t *testing.T) {
	_, fold := historicalFoldFixture(t, func(core *Core, key boundaryKey, prev NodeID) (condenseOutcome, error) {
		// A second, distinct SubtreeID with the exact same shape as the
		// survivor's own payload: reaches the structuralMatch >= 0 branch.
		duplicate := appendShallowPayload(t, core, shallowPayloadSpec{
			symbol: 20, productionID: 1, startByte: 12, endByte: 17, childSymbols: []Symbol{30},
		})
		return core.condenseWithOutcome(key, linkInput{
			prev: prev, payload: duplicate, scoreDelta: 99, order: ForkOrder{Present: true, Value: 2},
		})
	})
	if fold.change != condenseUnchanged {
		t.Fatalf("structural-match change=%v, want condenseUnchanged", fold.change)
	}
	assertNoFabricatedHistory(t, fold)
}

func TestFoldPrecedenceDropPropagatesProvenanceWithoutFabrication(t *testing.T) {
	_, fold := historicalFoldFixture(t, func(core *Core, key boundaryKey, prev NodeID) (condenseOutcome, error) {
		// Shallow-class-equal (same symbol/padding/size/childCount) but
		// structurally distinct (different production/child), with a
		// strictly lower aggregate score: reaches the
		// incomingPrecedence < incumbentPrecedence branch.
		dominated := appendShallowPayload(t, core, shallowPayloadSpec{
			symbol: 20, productionID: 2, startByte: 12, endByte: 17, childSymbols: []Symbol{31},
		})
		return core.condenseWithOutcome(key, linkInput{
			prev: prev, payload: dominated, scoreDelta: 1, order: ForkOrder{Present: true, Value: 2},
		})
	})
	if fold.change != condenseUnchanged {
		t.Fatalf("precedence-drop change=%v, want condenseUnchanged", fold.change)
	}
	assertNoFabricatedHistory(t, fold)
}

func TestFoldPrecedenceReplacePropagatesProvenanceWithoutFabrication(t *testing.T) {
	setup, fold := historicalFoldFixture(t, func(core *Core, key boundaryKey, prev NodeID) (condenseOutcome, error) {
		// Shallow-class-equal but structurally distinct, with a strictly
		// higher aggregate score: reaches the
		// incomingPrecedence > incumbentPrecedence replacement branch, which
		// republishes a brand-new node id via replaceBoundaryLink.
		dominant := appendShallowPayload(t, core, shallowPayloadSpec{
			symbol: 20, productionID: 3, startByte: 12, endByte: 17, childSymbols: []Symbol{32},
		})
		return core.condenseWithOutcome(key, linkInput{
			prev: prev, payload: dominant, scoreDelta: 99, order: ForkOrder{Present: true, Value: 2},
		})
	})
	if fold.change != condenseUpdated {
		t.Fatalf("precedence-replace change=%v, want condenseUpdated", fold.change)
	}
	if fold.head == setup.head {
		t.Fatalf("precedence-replace kept the historical survivor head %+v instead of republishing", setup.head)
	}
	assertNoFabricatedHistory(t, fold)
}

// TestDiagnosticShallowFoldStructuralMatchKeepsIncumbent is the plain
// (non-historical) counterpart of the fixture-based tests above: it proves
// the structuralMatch >= 0 branch itself -- survivor identity, no mutation
// of the incumbent's stored links, and the duplicate genuinely dropped
// rather than kept as a coexisting alternate. link_fold_test.go already
// covers the sibling precedence-drop/precedence-replace/tie branches
// (TestDiagnosticShallowFoldChildBearingParentSelectsHigherAggregateScore);
// this fills the one gap.
func TestDiagnosticShallowFoldStructuralMatchKeepsIncumbent(t *testing.T) {
	core, seed := newDiagnosticShallowFoldCore(t, Limits{MaxDerivations: 4})
	spec := shallowPayloadSpec{symbol: 20, productionID: 1, startByte: 12, endByte: 17, childSymbols: []Symbol{30}}
	incumbent := appendShallowPayload(t, core, spec)
	duplicate := appendShallowPayload(t, core, spec)
	if incumbent == duplicate {
		t.Fatalf("fixture reused one subtree id for two structurally-equal payloads")
	}
	key := core.boundaryKey(2, 17)
	oldHead, err := core.condense(key, linkInput{
		prev: seed.Node, payload: incumbent, scoreDelta: 5, order: ForkOrder{Present: true, Value: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	before, err := core.Stats(oldHead)
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := core.condenseWithOutcome(key, linkInput{
		prev: seed.Node, payload: duplicate, scoreDelta: 9, order: ForkOrder{Present: true, Value: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	if outcome.change != condenseUnchanged || outcome.head != oldHead {
		t.Fatalf("structural-match outcome=%+v, want unchanged head %+v", outcome, oldHead)
	}
	after, err := core.Stats(oldHead)
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("structural-match mutated incumbent storage: before=%+v after=%+v", before, after)
	}
	paths, err := core.Derivations(oldHead)
	if err != nil {
		t.Fatal(err)
	}
	want := []Derivation{{Payloads: []SubtreeID{incumbent}, Score: 5, BranchOrder: 1, HasBranchOrder: true}}
	if len(paths) != 1 || paths[0].Score != want[0].Score || paths[0].BranchOrder != want[0].BranchOrder ||
		len(paths[0].Payloads) != 1 || paths[0].Payloads[0] != incumbent {
		t.Fatalf("structural-match derivations=%#v, want %#v (duplicate must not survive as an alternate)", paths, want)
	}
}
