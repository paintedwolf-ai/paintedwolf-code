package gotreesitter

import "testing"

func TestCollectStackStatesIntoPreservesDeepEntryAndGSSOrder(t *testing.T) {
	const depth = 4096
	entries := make([]stackEntry, depth)
	want := make([]StateID, depth)
	for i := range entries {
		state := StateID(i + 1)
		entries[i].state = state
		want[i] = state
	}

	parser := &Parser{}
	entryStack := &glrStack{entries: entries}
	entryStates := parser.collectStackStatesInto(entryStack, make([]StateID, 0, depth))
	assertStateIDsEqual(t, entryStates, want)

	var gssStorage gssScratch
	gssStack := &glrStack{gss: buildGSSStack(entries, &gssStorage)}
	gssStates := parser.collectStackStatesInto(gssStack, make([]StateID, 0, depth))
	assertStateIDsEqual(t, gssStates, want)
}

func TestMissingShiftStateScratchReuseOverwritesActiveStateAndResetsLengths(t *testing.T) {
	parser := &Parser{}
	deepEntries := make([]stackEntry, 257)
	for i := range deepEntries {
		deepEntries[i].state = StateID(i + 100)
	}
	shallowEntries := []stackEntry{{state: 7}, {state: 11}, {state: 13}}

	var scratch missingShiftStateScratch
	scratch.baseStates = parser.collectStackStatesInto(&glrStack{entries: deepEntries}, scratch.baseStates[:0])
	deepBacking := &scratch.baseStates[0]
	deepCapacity := cap(scratch.baseStates)

	scratch.reset()
	if len(scratch.baseStates) != 0 || len(scratch.reduceStates) != 0 || len(scratch.simStates) != 0 {
		t.Fatalf("reset lengths = base:%d reduce:%d sim:%d, want all zero", len(scratch.baseStates), len(scratch.reduceStates), len(scratch.simStates))
	}
	scratch.baseStates = parser.collectStackStatesInto(&glrStack{entries: shallowEntries}, scratch.baseStates[:0])
	if got := len(scratch.baseStates); got != len(shallowEntries) {
		t.Fatalf("shallow length = %d, want %d", got, len(shallowEntries))
	}
	if &scratch.baseStates[0] != deepBacking || cap(scratch.baseStates) != deepCapacity {
		t.Fatal("shallow collection did not reuse the retained deep buffer")
	}
	assertStateIDsEqual(t, scratch.baseStates, []StateID{7, 11, 13})

	allocs := testing.AllocsPerRun(100, func() {
		scratch.baseStates = parser.collectStackStatesInto(&glrStack{entries: shallowEntries}, scratch.baseStates[:0])
	})
	if allocs != 0 {
		t.Fatalf("allocations with retained buffer = %v, want 0", allocs)
	}

	tracker := parseMissingShiftTracker{
		lastDepth:   9,
		consecutive: 3,
		stateScratch: missingShiftStateScratch{
			baseStates:   make([]StateID, 4, 16),
			reduceStates: make([]StateID, 3, 16),
			simStates:    make([]StateID, 2, 16),
		},
	}
	tracker.resetForToken()
	if tracker.lastDepth != -1 || tracker.consecutive != 0 {
		t.Fatalf("tracker reset = depth:%d consecutive:%d", tracker.lastDepth, tracker.consecutive)
	}
	if len(tracker.stateScratch.baseStates) != 0 || len(tracker.stateScratch.reduceStates) != 0 || len(tracker.stateScratch.simStates) != 0 {
		t.Fatal("tracker retained stale active StateIDs after token reset")
	}
	if cap(tracker.stateScratch.baseStates) != 16 || cap(tracker.stateScratch.reduceStates) != 16 || cap(tracker.stateScratch.simStates) != 16 {
		t.Fatal("tracker reset discarded reusable capacity")
	}
}

func TestMissingShiftScratchSimulationMatchesAllocationBacked(t *testing.T) {
	parser := NewParser(buildMissingShiftScratchDifferentialLanguage())
	tests := []struct {
		name               string
		baseStates         []StateID
		state              StateID
		wantCandidate      bool
		wantAfterReduction bool
	}{
		{
			name:          "direct candidate",
			baseStates:    []StateID{0, 5},
			state:         5,
			wantCandidate: true,
		},
		{
			name:       "candidate reduction cannot goto",
			baseStates: []StateID{0, 6},
			state:      6,
		},
		{
			name:               "candidate after one reduction",
			baseStates:         []StateID{0, 1},
			state:              1,
			wantAfterReduction: true,
		},
		{
			name:       "prelude reduction cannot goto",
			baseStates: []StateID{4, 1},
			state:      1,
		},
	}

	var scratch missingShiftStateScratch
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			allocSym, allocAction, allocOK := parser.findRecoverWithMissingShiftAtStates(tt.baseStates, tt.state, 3)
			scratchSym, scratchAction, scratchOK := parser.findRecoverWithMissingShiftAtStatesScratch(tt.baseStates, tt.state, 3, &scratch.simStates)
			assertMissingShiftSimulationEqual(t, nil, allocSym, allocAction, allocOK, nil, scratchSym, scratchAction, scratchOK)
			if allocOK != tt.wantCandidate {
				t.Fatalf("direct candidate result = %v, want %v", allocOK, tt.wantCandidate)
			}

			allocReductions, allocSym, allocAction, allocOK := parser.findRecoverWithMissingAfterReductionsAtStates(tt.baseStates, tt.state, 3, nil, nil)
			scratchReductions, scratchSym, scratchAction, scratchOK := parser.findRecoverWithMissingAfterReductionsAtStates(tt.baseStates, tt.state, 3, &scratch.reduceStates, &scratch.simStates)
			assertMissingShiftSimulationEqual(t, allocReductions, allocSym, allocAction, allocOK, scratchReductions, scratchSym, scratchAction, scratchOK)
			if allocOK != tt.wantAfterReduction {
				t.Fatalf("after-reduction candidate result = %v, want %v", allocOK, tt.wantAfterReduction)
			}

			if len(scratch.reduceStates) != 0 || len(scratch.simStates) != 0 {
				t.Fatalf("scratch lengths after simulation = reduce:%d sim:%d, want zero", len(scratch.reduceStates), len(scratch.simStates))
			}
		})
	}
}

func buildMissingShiftScratchDifferentialLanguage() *Language {
	const (
		missingToken     Symbol = 1
		reductionTrigger Symbol = 2
		lookahead        Symbol = 3
		preludeSymbol    Symbol = 4
		candidateSymbol  Symbol = 5
	)
	return &Language{
		SymbolCount: 6,
		TokenCount:  4,
		StateCount:  7,
		ParseActions: []ParseActionEntry{
			{},
			{Actions: []ParseAction{{Type: ParseActionShift, State: 2}}},
			{Actions: []ParseAction{{Type: ParseActionReduce, Symbol: preludeSymbol, ChildCount: 1}}},
			{Actions: []ParseAction{{Type: ParseActionShift, State: 5}}},
			{Actions: []ParseAction{{Type: ParseActionReduce, Symbol: candidateSymbol, ChildCount: 1}}},
			{Actions: []ParseAction{{Type: ParseActionShift, State: 3}}},
			{Actions: []ParseAction{{Type: ParseActionShift, State: 4}}},
		},
		ParseTable: [][]uint16{
			{0, 0, 0, 0, 3, 0},
			{0, 0, 2, 0, 0, 0},
			{0, 0, 0, 4, 0, 0},
			{0, 0, 0, 6, 0, 0},
			{0, 0, 0, 0, 0, 0},
			{0, 1, 0, 0, 0, 5},
			{0, 1, 0, 0, 0, 0},
		},
		SymbolNames: []string{"end", "missing", "reduce", "lookahead", "prelude", "candidate"},
		SymbolMetadata: []SymbolMetadata{
			{}, {}, {}, {}, {}, {},
		},
	}
}

func assertMissingShiftSimulationEqual(t *testing.T, wantReductions []ParseAction, wantSym Symbol, wantAction ParseAction, wantOK bool, gotReductions []ParseAction, gotSym Symbol, gotAction ParseAction, gotOK bool) {
	t.Helper()
	if gotOK != wantOK || gotSym != wantSym || gotAction != wantAction {
		t.Fatalf("scratch result = (sym:%d action:%+v ok:%v), allocation-backed = (sym:%d action:%+v ok:%v)", gotSym, gotAction, gotOK, wantSym, wantAction, wantOK)
	}
	if len(gotReductions) != len(wantReductions) {
		t.Fatalf("scratch reductions = %+v, allocation-backed = %+v", gotReductions, wantReductions)
	}
	for i := range wantReductions {
		if gotReductions[i] != wantReductions[i] {
			t.Fatalf("scratch reduction[%d] = %+v, allocation-backed = %+v", i, gotReductions[i], wantReductions[i])
		}
	}
}

func assertStateIDsEqual(t *testing.T, got, want []StateID) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("state count = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("state[%d] = %d, want %d", i, got[i], want[i])
		}
	}
}
