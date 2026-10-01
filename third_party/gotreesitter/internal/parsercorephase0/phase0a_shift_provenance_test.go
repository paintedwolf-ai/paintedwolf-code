//go:build gts_parsercorephase0 && gts_parsercorephase0a

package parsercorephase0

import (
	"errors"
	"math"
	"reflect"
	"testing"
)

func phase0AShiftProvenanceLimits() Phase0AObserverLimits {
	return Phase0AObserverLimits{
		MaxCores: 4, MaxRecords: 512, MaxBytes: 128 << 10,
		MaxFrames: 16, MaxMutations: 256,
		MaxOccurrences: 64, MaxOccurrenceBytes: 64 << 10,
	}
}

func phase0ABeginShiftProvenanceRun(t *testing.T, core *Core, limits Phase0AObserverLimits) CoreRunNamespace {
	t.Helper()
	phase0ATestSession(t, limits)
	run, err := core.BeginRun()
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func phase0ATwoShiftFixture(t *testing.T, extra bool, limits Limits) (*Core, []Head, []ClassifiedBoundary) {
	t.Helper()
	action := func(state StateID) Action {
		return Action{Type: ActionShift, State: state, Extra: extra}
	}
	tables := &fakeTable{actions: map[tableCell][]Action{
		{state: 1, symbol: 9}: {action(3)},
		{state: 2, symbol: 9}: {action(3)},
	}}
	core, err := New(tables, limits)
	if err != nil {
		t.Fatal(err)
	}
	heads := make([]Head, 2)
	boundaries := make([]ClassifiedBoundary, 2)
	for index, state := range []StateID{1, 2} {
		heads[index], err = core.Seed(state, 0)
		if err != nil {
			t.Fatal(err)
		}
		boundaries[index], err = core.ClassifyBoundary(heads[index], 9)
		if err != nil {
			t.Fatal(err)
		}
	}
	return core, heads, boundaries
}

func phase0ARequireTerminalProof(t *testing.T, proof Phase0AProofSnapshot, count int, kind Phase0AConstructionKind, predecessors []NodeID) []Phase0AMutationRecord {
	t.Helper()
	wantMutations := 1 + 2*count
	if len(proof.Mutations) != wantMutations || proof.OccurrenceCount != uint64(count) || proof.OccurrenceBytes != uint64(count)*(phase0AOccurrenceRecordBytes+phase0AEdgeRecordBytes) {
		t.Fatalf("proof counts mutations=%d occurrences=%d bytes=%d, want %d/%d/%d: %+v", len(proof.Mutations), proof.OccurrenceCount, proof.OccurrenceBytes, wantMutations, count, uint64(count)*(phase0AOccurrenceRecordBytes+phase0AEdgeRecordBytes), proof.Mutations)
	}
	event := proof.Mutations[0]
	if event.Kind != Phase0AMutationEvent || event.Event.Serial == 0 || event.Payload == 0 || event.ConstructionKind != kind || event.RolledBack {
		t.Fatalf("event = %+v", event)
	}
	occurrences := make([]Phase0AMutationRecord, count)
	for index := 0; index < count; index++ {
		occurrence := proof.Mutations[1+2*index]
		edge := proof.Mutations[2+2*index]
		if occurrence.Kind != Phase0AMutationOccurrence || edge.Kind != Phase0AMutationEdge ||
			occurrence.Event != event.Event || edge.Event != event.Event ||
			occurrence.Occurrence != edge.Occurrence || occurrence.Edge != edge.Edge ||
			occurrence.Occurrence.Slot != uint32(index+1) || occurrence.Edge.Serial == 0 ||
			occurrence.Payload != event.Payload || edge.Payload != event.Payload ||
			occurrence.Predecessor != predecessors[index] || edge.Predecessor != predecessors[index] ||
			occurrence.ConstructionKind != kind || edge.ConstructionKind != kind ||
			occurrence.RolledBack || edge.RolledBack {
			t.Fatalf("occurrence %d = %+v edge=%+v event=%+v", index, occurrence, edge, event)
		}
		if index > 0 && occurrence.Edge.Serial <= occurrences[index-1].Edge.Serial {
			t.Fatalf("incoming edge order is not stable: previous=%+v current=%+v", occurrences[index-1].Edge, occurrence.Edge)
		}
		occurrences[index] = occurrence
	}
	return occurrences
}

func TestPhase0ASingleTerminalShiftConstructionProvenance(t *testing.T) {
	core, seed, boundary := newSchedulerTransactionShiftFixture(t)
	run := phase0ABeginShiftProvenanceRun(t, core, phase0AShiftProvenanceLimits())
	if _, err := core.ShiftClassified(boundary, 0, Token{Symbol: 9, EndByte: 1}, ForkOrder{}); err != nil {
		t.Fatal(err)
	}
	proof, err := Phase0AObserverProof(core, run)
	if err != nil {
		t.Fatal(err)
	}
	occurrences := phase0ARequireTerminalProof(t, proof, 1, Phase0AConstructionOrdinaryTerminal, []NodeID{seed.Node})
	record, err := Phase0AResolveCommittedConstructionOccurrence(core, run, occurrences[0].Occurrence)
	if err != nil || record != occurrences[0] {
		t.Fatalf("resolved occurrence=%+v err=%v, want %+v", record, err, occurrences[0])
	}
}

func TestPhase0ATerminalCohortConstructionProvenance(t *testing.T) {
	for _, test := range []struct {
		name  string
		extra bool
		kind  Phase0AConstructionKind
	}{
		{name: "ordinary", kind: Phase0AConstructionOrdinaryTerminal},
		{name: "extra", extra: true, kind: Phase0AConstructionExtraTerminal},
	} {
		t.Run(test.name, func(t *testing.T) {
			core, heads, boundaries := phase0ATwoShiftFixture(t, test.extra, Limits{})
			run := phase0ABeginShiftProvenanceRun(t, core, phase0AShiftProvenanceLimits())
			token := Token{Symbol: 9, EndByte: 1, Extra: test.extra}
			var err error
			if test.extra {
				_, err = core.ShiftExtraClassifiedCohort(boundaries, token)
			} else {
				_, err = core.ShiftOrdinaryClassifiedCohort(boundaries, token)
			}
			if err != nil {
				t.Fatal(err)
			}
			proof, err := Phase0AObserverProof(core, run)
			if err != nil {
				t.Fatal(err)
			}
			occurrences := phase0ARequireTerminalProof(t, proof, 2, test.kind, []NodeID{heads[0].Node, heads[1].Node})
			if occurrences[0].Payload != occurrences[1].Payload || occurrences[0].Occurrence == occurrences[1].Occurrence || occurrences[0].Edge == occurrences[1].Edge {
				t.Fatalf("cohort did not preserve shared payload/distinct uses: %+v", occurrences)
			}
		})
	}
}

func TestPhase0AStandaloneAndSchedulerOwnedShiftObserveExactlyOnce(t *testing.T) {
	for _, owned := range []bool{false, true} {
		t.Run(map[bool]string{false: "standalone", true: "scheduler-owned"}[owned], func(t *testing.T) {
			core, seed, boundary := newSchedulerTransactionShiftFixture(t)
			run := phase0ABeginShiftProvenanceRun(t, core, phase0AShiftProvenanceLimits())
			if owned {
				if err := core.ApplySchedulerAtomic(func(owner SchedulerTransactionToken) error {
					_, err := core.ShiftClassifiedOwned(owner, boundary, 0, Token{Symbol: 9, EndByte: 1}, ForkOrder{})
					return err
				}); err != nil {
					t.Fatal(err)
				}
			} else if _, err := core.ShiftClassified(boundary, 0, Token{Symbol: 9, EndByte: 1}, ForkOrder{}); err != nil {
				t.Fatal(err)
			}
			proof, err := Phase0AObserverProof(core, run)
			if err != nil {
				t.Fatal(err)
			}
			phase0ARequireTerminalProof(t, proof, 1, Phase0AConstructionOrdinaryTerminal, []NodeID{seed.Node})
		})
	}
}

func TestPhase0AShiftOccurrenceRollbackLifecycle(t *testing.T) {
	t.Run("direct-shift-rollback", func(t *testing.T) {
		tables := &fakeTable{actions: map[tableCell][]Action{
			{state: 1, symbol: 9}: {{Type: ActionShift, State: 3}},
			{state: 2, symbol: 9}: {{Type: ActionShift, State: 4}},
		}}
		core, err := New(tables, Limits{MaxLinks: 1})
		if err != nil {
			t.Fatal(err)
		}
		first, _ := core.Seed(1, 0)
		second, _ := core.Seed(2, 0)
		firstBoundary, _ := core.ClassifyBoundary(first, 9)
		secondBoundary, _ := core.ClassifyBoundary(second, 9)
		run := phase0ABeginShiftProvenanceRun(t, core, phase0AShiftProvenanceLimits())
		if _, err := core.ShiftClassified(firstBoundary, 0, Token{Symbol: 9, EndByte: 1}, ForkOrder{}); err != nil {
			t.Fatal(err)
		}
		if _, err := core.ShiftClassified(secondBoundary, 0, Token{Symbol: 9, EndByte: 1}, ForkOrder{}); err == nil {
			t.Fatal("second shift did not reach the configured link cap")
		}
		proof, err := Phase0AObserverProof(core, run)
		if err != nil {
			t.Fatal(err)
		}
		if len(proof.Mutations) != 6 {
			t.Fatalf("mutations=%+v", proof.Mutations)
		}
		for index, mutation := range proof.Mutations {
			if mutation.RolledBack != (index >= 3) {
				t.Fatalf("rollback split at %d: %+v", index, proof.Mutations)
			}
		}
		rolledBack := proof.Mutations[4].Occurrence
		if _, err := Phase0AResolveCommittedConstructionOccurrence(core, run, rolledBack); !isPhase0AError(err, Phase0AErrorInvalidOccurrence, "") {
			t.Fatalf("rolled-back occurrence resolved live: %v", err)
		}
	})

	t.Run("inner-commit-outer-rollback", func(t *testing.T) {
		core, _, boundary := newSchedulerTransactionShiftFixture(t)
		run := phase0ABeginShiftProvenanceRun(t, core, phase0AShiftProvenanceLimits())
		sentinel := errors.New("outer rollback")
		if err := core.ApplyAtomic(func() error {
			if _, err := core.ShiftClassified(boundary, 0, Token{Symbol: 9, EndByte: 1}, ForkOrder{}); err != nil {
				return err
			}
			return sentinel
		}); !errors.Is(err, sentinel) {
			t.Fatalf("outer rollback=%v", err)
		}
		proof, err := Phase0AObserverProof(core, run)
		if err != nil {
			t.Fatal(err)
		}
		for _, mutation := range proof.Mutations {
			if !mutation.RolledBack || mutation.RollbackCause != Phase0ARollbackReturnedError || mutation.RollbackTransaction == mutation.TransactionID {
				t.Fatalf("outer rollback did not tombstone committed inner occurrence: %+v", mutation)
			}
		}
	})
}

func TestPhase0AShiftOccurrenceCapsAreAtomicStickyAndNonSemantic(t *testing.T) {
	tests := []struct {
		name   string
		adjust func(*Phase0AObserverLimits)
		kind   Phase0AErrorKind
	}{
		{name: "occurrence-count", kind: Phase0AErrorOccurrenceCap, adjust: func(limits *Phase0AObserverLimits) { limits.MaxOccurrences = 1 }},
		{name: "occurrence-bytes", kind: Phase0AErrorOccurrenceByteCap, adjust: func(limits *Phase0AObserverLimits) {
			limits.MaxOccurrenceBytes = 2*(phase0AOccurrenceRecordBytes+phase0AEdgeRecordBytes) - 1
		}},
		{name: "total-bytes", kind: Phase0AErrorByteCap, adjust: func(limits *Phase0AObserverLimits) {
			limits.MaxBytes = phase0AEventRecordBytes + 2*(phase0AOccurrenceRecordBytes+phase0AEdgeRecordBytes) - 1
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			core, _, boundaries := phase0ATwoShiftFixture(t, false, Limits{})
			limits := phase0AShiftProvenanceLimits()
			test.adjust(&limits)
			run := phase0ABeginShiftProvenanceRun(t, core, limits)
			heads, err := core.ShiftOrdinaryClassifiedCohort(boundaries, Token{Symbol: 9, EndByte: 1})
			if err != nil || len(heads) != 2 || core.Work().Shifts != 2 {
				t.Fatalf("observer cap changed parser execution heads=%+v work=%+v err=%v", heads, core.Work(), err)
			}
			proof, proofErr := Phase0AObserverProof(core, run)
			if !isPhase0AError(proofErr, test.kind, "") || proof.Failure == nil || proof.Failure.Kind != test.kind || len(proof.Mutations) != 0 || proof.OccurrenceCount != 0 || proof.OccurrenceBytes != 0 {
				t.Fatalf("cap was partial or non-sticky proof=%+v err=%v", proof, proofErr)
			}
			phase0AObservers.Lock()
			observer := phase0AObservers.byCore[core]
			nextEvent, nextEdge := observer.nextEvent, observer.nextEdge
			phase0AObservers.Unlock()
			if nextEvent != 0 || nextEdge != 0 {
				t.Fatalf("failed atomic reservation advanced serials event=%d edge=%d", nextEvent, nextEdge)
			}
		})
	}
}

func TestPhase0AShiftOccurrenceResetStaleAndSlotOverflow(t *testing.T) {
	t.Run("reset-and-new-run-release", func(t *testing.T) {
		core, _, boundary := newSchedulerTransactionShiftFixture(t)
		run := phase0ABeginShiftProvenanceRun(t, core, phase0AShiftProvenanceLimits())
		if _, err := core.ShiftClassified(boundary, 0, Token{Symbol: 9, EndByte: 1}, ForkOrder{}); err != nil {
			t.Fatal(err)
		}
		proof, _ := Phase0AObserverProof(core, run)
		old := proof.Mutations[1].Occurrence
		if err := core.Reset(); err != nil {
			t.Fatal(err)
		}
		phase0AObservers.Lock()
		observer := phase0AObservers.byCore[core]
		if observer.active || observer.occurrenceIndex != nil || observer.occurrenceCount != 0 || observer.occurrenceBytes != 0 {
			phase0AObservers.Unlock()
			t.Fatalf("reset retained occurrence mappings/capacity: %+v", observer)
		}
		phase0AObservers.Unlock()
		if _, err := Phase0AResolveCommittedConstructionOccurrence(core, run, old); !isPhase0AError(err, Phase0AErrorStaleNamespace, "") {
			t.Fatalf("old occurrence did not become stale: %v", err)
		}
		newRun, err := core.BeginRun()
		if err != nil {
			t.Fatal(err)
		}
		if newRun == run {
			t.Fatalf("run namespace reused: %+v", run)
		}
		phase0AObservers.Lock()
		observer = phase0AObservers.byCore[core]
		empty := len(observer.occurrenceIndex) == 0 && observer.occurrenceCount == 0 && observer.occurrenceBytes == 0
		phase0AObservers.Unlock()
		if !empty {
			t.Fatal("BeginRun retained prior occurrence state")
		}
	})

	t.Run("slot-overflow-before-mutation", func(t *testing.T) {
		core := phase0ATestCore(t)
		run := phase0ABeginShiftProvenanceRun(t, core, phase0AShiftProvenanceLimits())
		phase0AObservers.Lock()
		observer := phase0AObservers.byCore[core]
		phase0AObserveTerminalConstructionLocked(core, observer, 1, Phase0AConstructionOrdinaryTerminal, uint64(math.MaxUint32)+1, func(uint64) NodeID { return 1 }, func(uint64) boundaryKey { return boundaryKey{} }, func(uint64) int64 { return 0 }, func(uint64) ForkOrder { return ForkOrder{} })
		phase0AObservers.Unlock()
		proof, err := Phase0AObserverProof(core, run)
		if !isPhase0AError(err, Phase0AErrorCounterOverflow, Phase0ACounterOccurrenceSlot) || len(proof.Mutations) != 0 || proof.OccurrenceCount != 0 {
			t.Fatalf("slot overflow mutated observer proof=%+v err=%v", proof, err)
		}
	})
}

func TestPhase0ACommittedConstructionResolverIncludesDuplicateNoopAttempt(t *testing.T) {
	core, seed, boundary := newSchedulerTransactionShiftFixture(t)
	run := phase0ABeginShiftProvenanceRun(t, core, phase0AShiftProvenanceLimits())
	first, err := core.ShiftClassified(boundary, 0, Token{Symbol: 9, EndByte: 1}, ForkOrder{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := core.ShiftClassified(boundary, 0, Token{Symbol: 9, EndByte: 1}, ForkOrder{})
	if err != nil {
		t.Fatal(err)
	}
	if first != second || core.Work().PredecessorLinkUnionDuplicateNoop != 1 {
		t.Fatalf("second construction was not a shallow duplicate/no-op: first=%+v second=%+v work=%+v", first, second, core.Work())
	}
	proof, err := Phase0AObserverProof(core, run)
	if err != nil || len(proof.Mutations) != 6 {
		t.Fatalf("proof=%+v err=%v", proof, err)
	}
	secondOccurrence := proof.Mutations[4]
	if secondOccurrence.Predecessor != seed.Node {
		t.Fatalf("duplicate occurrence predecessor=%d, want %d", secondOccurrence.Predecessor, seed.Node)
	}
	resolved, err := Phase0AResolveCommittedConstructionOccurrence(core, run, secondOccurrence.Occurrence)
	if err != nil || resolved != secondOccurrence {
		t.Fatalf("committed duplicate attempt=%+v err=%v, want %+v", resolved, err, secondOccurrence)
	}
}

func TestPhase0ATerminalHookAuthenticationFailuresAreStickyAndNonSemantic(t *testing.T) {
	newFixture := func(t *testing.T, record subtreeRecord) (*Core, CoreRunNamespace, Head, SubtreeID) {
		t.Helper()
		core := phase0ATestCore(t)
		seed, err := core.Seed(1, 0)
		if err != nil {
			t.Fatal(err)
		}
		payload, err := core.appendSubtree(record, nil, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		run := phase0ABeginShiftProvenanceRun(t, core, phase0AShiftProvenanceLimits())
		return core, run, seed, payload
	}
	requireFailure := func(t *testing.T, core *Core, run CoreRunNamespace, before schedulerTransactionState) {
		t.Helper()
		proof, err := Phase0AObserverProof(core, run)
		if !isPhase0AError(err, Phase0AErrorInvalidOccurrence, "") || proof.Failure == nil || proof.Failure.Kind != Phase0AErrorInvalidOccurrence || len(proof.Mutations) != 0 || proof.OccurrenceCount != 0 {
			t.Fatalf("authentication failure proof=%+v err=%v", proof, err)
		}
		if after := captureSchedulerTransactionState(core); !reflect.DeepEqual(after, before) {
			t.Fatalf("observer authentication failure changed parser state: before=%+v after=%+v", before, after)
		}
	}

	t.Run("payload-does-not-exist", func(t *testing.T) {
		core, run, seed, _ := newFixture(t, subtreeRecord{symbol: 9, endByte: 1, terminal: true})
		before := captureSchedulerTransactionState(core)
		phase0AObserveTerminalShift(core, SubtreeID(len(core.subtrees)+1), seed.Node, 2, 1, false, false, 0, ForkOrder{})
		requireFailure(t, core, run, before)
	})
	t.Run("payload-is-not-terminal", func(t *testing.T) {
		core, run, seed, payload := newFixture(t, subtreeRecord{symbol: 9, endByte: 1})
		before := captureSchedulerTransactionState(core)
		phase0AObserveTerminalShift(core, payload, seed.Node, 2, 1, false, false, 0, ForkOrder{})
		requireFailure(t, core, run, before)
	})
	t.Run("payload-extra-kind-mismatch", func(t *testing.T) {
		core, run, seed, payload := newFixture(t, subtreeRecord{symbol: 9, endByte: 1, terminal: true})
		before := captureSchedulerTransactionState(core)
		phase0AObserveTerminalShift(core, payload, seed.Node, 2, 1, false, true, 0, ForkOrder{})
		requireFailure(t, core, run, before)
	})
	for _, test := range []struct {
		name        string
		predecessor func(*Core) NodeID
	}{
		{name: "zero-predecessor", predecessor: func(*Core) NodeID { return 0 }},
		{name: "unknown-predecessor", predecessor: func(core *Core) NodeID { return NodeID(len(core.nodes) + 1) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			core, run, _, payload := newFixture(t, subtreeRecord{symbol: 9, endByte: 1, terminal: true})
			before := captureSchedulerTransactionState(core)
			phase0AObserveTerminalShift(core, payload, test.predecessor(core), 2, 1, false, false, 0, ForkOrder{})
			requireFailure(t, core, run, before)
		})
	}
	t.Run("cohort-boundary-belongs-to-another-core", func(t *testing.T) {
		core, run, _, payload := newFixture(t, subtreeRecord{symbol: 9, endByte: 1, terminal: true})
		foreign, _, boundary := newSchedulerTransactionShiftFixture(t)
		before := captureSchedulerTransactionState(core)
		phase0AObserveTerminalCohortShift(core, payload, []ClassifiedBoundary{boundary}, []StateID{2}, 1, false)
		requireFailure(t, core, run, before)
		if foreign == core {
			t.Fatal("foreign fixture unexpectedly reused core")
		}
	})
}

func TestPhase0AShiftWithoutSessionIsNoop(t *testing.T) {
	core, _, boundary := newSchedulerTransactionShiftFixture(t)
	if _, err := core.ShiftClassified(boundary, 0, Token{Symbol: 9, EndByte: 1}, ForkOrder{}); err != nil || core.Work().Shifts != 1 {
		t.Fatalf("no-session hook changed parser execution work=%+v err=%v", core.Work(), err)
	}
}
