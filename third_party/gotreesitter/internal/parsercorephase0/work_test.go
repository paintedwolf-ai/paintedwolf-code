package parsercorephase0

import (
	"errors"
	"math"
	"testing"
)

func TestWorkCountsCommittedShiftCohortAndReduction(t *testing.T) {
	t.Run("diagnostic-payload-is-not-a-shift", func(t *testing.T) {
		compact, err := New(&fakeTable{}, Limits{})
		if err != nil {
			t.Fatal(err)
		}
		seed, _ := compact.Seed(1, 0)
		if _, err := compact.appendDiagnosticPayload(seed, 2, Token{Symbol: 9, EndByte: 1}, pathMeta{}); err != nil {
			t.Fatal(err)
		}
		want := Work{GraphLinkAdditionsProxy: 1, LeafConstructionsProxy: 1}
		if got := compact.Work(); got != want {
			t.Fatalf("diagnostic payload work=%+v, want %+v", got, want)
		}
	})

	t.Run("single-shift-and-reduction", func(t *testing.T) {
		tables := &fakeTable{
			actions: map[tableCell][]Action{
				{state: 1, symbol: 9}:  {{Type: ActionShift, State: 2}},
				{state: 2, symbol: 10}: {{Type: ActionReduce, Symbol: 4, ChildCount: 1}},
			},
			gotos: map[tableCell]StateID{{state: 1, symbol: 4}: 3},
		}
		compact, err := New(tables, Limits{MaxDerivations: 4, MaxPopPaths: 4})
		if err != nil {
			t.Fatal(err)
		}
		seed, err := compact.Seed(1, 0)
		if err != nil {
			t.Fatal(err)
		}
		shifted, err := compact.Shift(seed, 9, 0, Token{Symbol: 9, EndByte: 1}, ForkOrder{})
		if err != nil {
			t.Fatal(err)
		}
		wantShift := Work{Shifts: 1, GraphLinkAdditionsProxy: 1, LeafConstructionsProxy: 1}
		if got := compact.Work(); got != wantShift {
			t.Fatalf("shift work=%+v, want %+v", got, wantShift)
		}
		frontier, err := compact.ReduceOutputs(shifted, 10, 0, ForkOrder{})
		if err != nil || len(frontier) != 1 {
			t.Fatalf("reduction frontier=%+v err=%v", frontier, err)
		}
		want := Work{
			Shifts: 1, Reductions: 1, ReductionPopRequests: 1,
			EmittedPopPaths: 1, EmittedPopPayloads: 1,
			GraphLinkAdditionsProxy: 2, LeafConstructionsProxy: 1,
			ParentConstructionsProxy: 1,
		}
		if got := compact.Work(); got != want {
			t.Fatalf("reduction work=%+v, want %+v", got, want)
		}
		stats, err := compact.Stats(frontier[0].Head)
		if err != nil {
			t.Fatal(err)
		}
		if uint64(stats.Links) != want.GraphLinkAdditionsProxy || uint64(stats.Subtrees) != want.LeafConstructionsProxy+want.ParentConstructionsProxy {
			t.Fatalf("work/storage mismatch: work=%+v stats=%+v", want, stats)
		}
	})

	t.Run("ordinary-cohort", func(t *testing.T) {
		tables := &fakeTable{actions: map[tableCell][]Action{
			{state: 1, symbol: 9}: {{Type: ActionShift, State: 3}},
			{state: 2, symbol: 9}: {{Type: ActionShift, State: 3}},
		}}
		compact, err := New(tables, Limits{MaxDerivations: 4, MaxPopPaths: 4})
		if err != nil {
			t.Fatal(err)
		}
		first, _ := compact.Seed(1, 0)
		second, _ := compact.Seed(2, 0)
		heads, err := compact.ShiftOrdinaryCohort([]OrdinaryCohortShiftInput{{Head: first}, {Head: second}}, 9, Token{Symbol: 9, EndByte: 1})
		if err != nil || len(heads) != 2 {
			t.Fatalf("cohort heads=%+v err=%v", heads, err)
		}
		want := Work{
			Shifts: 2, GraphLinkAdditionsProxy: 2, LeafConstructionsProxy: 1,
			PredecessorLinkUnionAttempts: 1, PredecessorLinkUnionAlternateAppended: 1,
		}
		if got := compact.Work(); got != want {
			t.Fatalf("cohort work=%+v, want %+v", got, want)
		}
		stats, err := compact.Stats(heads[1])
		if err != nil {
			t.Fatal(err)
		}
		if uint64(stats.Links) != want.GraphLinkAdditionsProxy || uint64(stats.Subtrees) != want.LeafConstructionsProxy {
			t.Fatalf("cohort work/storage mismatch: work=%+v stats=%+v", want, stats)
		}
	})
}

func TestWorkRollbackAndOverflowAreTransactional(t *testing.T) {
	t.Run("nested-outer-rollback", func(t *testing.T) {
		tables := &fakeTable{actions: map[tableCell][]Action{
			{state: 1, symbol: 9}: {{Type: ActionShift, State: 2}},
		}}
		compact, err := New(tables, Limits{})
		if err != nil {
			t.Fatal(err)
		}
		seed, _ := compact.Seed(1, 0)
		sentinel := errors.New("rollback")
		err = compact.ApplyAtomic(func() error {
			if _, err := compact.Shift(seed, 9, 0, Token{Symbol: 9, EndByte: 1}, ForkOrder{}); err != nil {
				return err
			}
			return sentinel
		})
		if !errors.Is(err, sentinel) || compact.Work() != (Work{}) {
			t.Fatalf("outer rollback err=%v work=%+v", err, compact.Work())
		}
	})

	t.Run("cohort-cap-rollback", func(t *testing.T) {
		tables := &fakeTable{actions: map[tableCell][]Action{
			{state: 1, symbol: 9}: {{Type: ActionShift, State: 3}},
			{state: 2, symbol: 9}: {{Type: ActionShift, State: 3}},
		}}
		compact, err := New(tables, Limits{MaxLinks: 1})
		if err != nil {
			t.Fatal(err)
		}
		first, _ := compact.Seed(1, 0)
		second, _ := compact.Seed(2, 0)
		if _, err := compact.ShiftOrdinaryCohort([]OrdinaryCohortShiftInput{{Head: first}, {Head: second}}, 9, Token{Symbol: 9, EndByte: 1}); err == nil {
			t.Fatal("capped cohort unexpectedly succeeded")
		}
		if got := compact.Work(); got != (Work{}) {
			t.Fatalf("capped cohort leaked work=%+v", got)
		}
	})

	t.Run("saturating-overflow", func(t *testing.T) {
		tables := &fakeTable{actions: map[tableCell][]Action{
			{state: 1, symbol: 9}: {{Type: ActionShift, State: 3}},
			{state: 2, symbol: 9}: {{Type: ActionShift, State: 3}},
		}}
		compact, err := New(tables, Limits{})
		if err != nil {
			t.Fatal(err)
		}
		first, _ := compact.Seed(1, 0)
		second, _ := compact.Seed(2, 0)
		compact.work.Shifts = math.MaxUint64 - 1
		if _, err := compact.ShiftOrdinaryCohort([]OrdinaryCohortShiftInput{{Head: first}, {Head: second}}, 9, Token{Symbol: 9, EndByte: 1}); err != nil {
			t.Fatal(err)
		}
		got := compact.Work()
		if got.Shifts != math.MaxUint64 || !got.Overflow {
			t.Fatalf("overflow work=%+v", got)
		}
	})
}

func TestLinkUnionWorkPartitionsSemanticOutcomes(t *testing.T) {
	compact, seed := newDiagnosticShallowFoldCore(t, Limits{MaxDerivations: 8})
	incumbent := appendShallowPayload(t, compact, shallowPayloadSpec{
		symbol: 20, productionID: 1, startByte: 12, endByte: 17, childSymbols: []Symbol{30},
	})
	key := compact.boundaryKey(2, 17)
	if _, err := compact.condense(key, linkInput{prev: seed.Node, payload: incumbent, scoreDelta: 2}); err != nil {
		t.Fatal(err)
	}
	if got := compact.Work(); got.PredecessorLinkUnionAttempts != 0 || got.PredecessorLinkUnionAlternateAppended != 0 {
		t.Fatalf("primary publication entered union counters: %+v", got)
	}
	if _, err := compact.condense(key, linkInput{prev: seed.Node, payload: incumbent, scoreDelta: 2}); err != nil {
		t.Fatal(err)
	}
	higher := appendShallowPayload(t, compact, shallowPayloadSpec{
		symbol: 20, productionID: 2, startByte: 12, endByte: 17, childSymbols: []Symbol{31},
	})
	if _, err := compact.condense(key, linkInput{prev: seed.Node, payload: higher, scoreDelta: 3}); err != nil {
		t.Fatal(err)
	}
	distinct := appendShallowPayload(t, compact, shallowPayloadSpec{symbol: 21, startByte: 12, endByte: 17})
	if _, err := compact.condense(key, linkInput{prev: seed.Node, payload: distinct}); err != nil {
		t.Fatal(err)
	}
	work := compact.Work()
	if work.PredecessorLinkUnionAttempts != 3 ||
		work.PredecessorLinkUnionDuplicateNoop != 1 ||
		work.PredecessorLinkUnionPrecedenceReplaced != 1 ||
		work.PredecessorLinkUnionAlternateAppended != 1 ||
		work.PredecessorLinkUnionRecursiveChanged != 0 ||
		work.PredecessorLinkUnionRejected != 0 {
		t.Fatalf("link-union partition=%+v", work)
	}
	outcomes := work.PredecessorLinkUnionDuplicateNoop +
		work.PredecessorLinkUnionPrecedenceReplaced +
		work.PredecessorLinkUnionRecursiveChanged +
		work.PredecessorLinkUnionAlternateAppended +
		work.PredecessorLinkUnionRejected
	if outcomes != work.PredecessorLinkUnionAttempts {
		t.Fatalf("link-union outcomes=%d attempts=%d", outcomes, work.PredecessorLinkUnionAttempts)
	}
}

func TestRawSelectedSubtreeCensusCountsOccurrencesBeforeMaterialization(t *testing.T) {
	compact := newTinyCoreWithLimits(t, Limits{})
	leaf, err := compact.appendSubtree(subtreeRecord{symbol: 9, endByte: 1, terminal: true}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	parent, err := compact.appendSubtree(subtreeRecord{symbol: 10, endByte: 1}, []SubtreeID{leaf, leaf}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	census, err := compact.RawSelectedSubtreeCensus([]SubtreeID{parent})
	if err != nil {
		t.Fatal(err)
	}
	if census != (RawSelectedCensus{Nodes: 3, Parents: 1, Leaves: 2}) {
		t.Fatalf("raw selected census=%+v", census)
	}
}

func TestRawSelectedCensusFreezesSpeculativeConstructionSurplus(t *testing.T) {
	compact := newTinyCoreWithLimits(t, Limits{})
	selectedLeaf, err := compact.appendSubtree(subtreeRecord{symbol: 9, endByte: 1, terminal: true}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	losingLeaf, err := compact.appendSubtree(subtreeRecord{symbol: 10, endByte: 1, terminal: true}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	selectedParent, err := compact.appendSubtree(subtreeRecord{symbol: 11, endByte: 1}, []SubtreeID{selectedLeaf}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := compact.appendSubtree(subtreeRecord{symbol: 12, endByte: 1}, []SubtreeID{losingLeaf}, nil, nil); err != nil {
		t.Fatal(err)
	}
	census, err := compact.RawSelectedSubtreeCensus([]SubtreeID{selectedParent})
	if err != nil {
		t.Fatal(err)
	}
	work := compact.Work()
	if census != (RawSelectedCensus{Nodes: 2, Parents: 1, Leaves: 1}) ||
		work.ParentConstructionsProxy-census.Parents != 1 ||
		work.LeafConstructionsProxy-census.Leaves != 1 {
		t.Fatalf("construction surplus work=%+v census=%+v", work, census)
	}
}
