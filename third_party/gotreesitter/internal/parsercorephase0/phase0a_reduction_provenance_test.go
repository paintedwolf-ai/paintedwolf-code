//go:build gts_parsercorephase0 && gts_parsercorephase0a

package parsercorephase0

import (
	"errors"
	"testing"
)

func phase0AReductionLimits() Phase0AObserverLimits {
	return Phase0AObserverLimits{
		MaxCores: 4, MaxRecords: 1024, MaxBytes: 256 << 10,
		MaxFrames: 32, MaxMutations: 512,
		MaxOccurrences: 128, MaxOccurrenceBytes: 128 << 10,
	}
}

func TestPhase0AReductionPopPathsAllocateImmediateOccurrences(t *testing.T) {
	core, head := newSharedReductionFixture(t, true)
	run := phase0ABeginShiftProvenanceRun(t, core, phase0AReductionLimits())
	frontier, err := core.Reduce(head, 9, 0, ForkOrder{})
	if err != nil || len(frontier) != 2 {
		t.Fatalf("reduce frontier=%v err=%v", frontier, err)
	}
	proof, err := Phase0AObserverProof(core, run)
	if err != nil {
		t.Fatal(err)
	}
	if len(proof.Mutations) != 5 || len(proof.ReductionOccurrences) != 2 || proof.OccurrenceCount != 2 {
		t.Fatalf("proof mutations=%+v reduction=%+v occurrences=%d", proof.Mutations, proof.ReductionOccurrences, proof.OccurrenceCount)
	}
	event := proof.Mutations[0]
	if event.Kind != Phase0AMutationEvent || event.ConstructionKind != Phase0AConstructionReductionParent || event.Payload == 0 || event.RolledBack {
		t.Fatalf("event=%+v", event)
	}
	wantStates := map[StateID]bool{4: false, 5: false}
	for index := range 2 {
		occurrence := proof.Mutations[1+2*index]
		edge := proof.Mutations[2+2*index]
		sidecar := proof.ReductionOccurrences[index]
		if occurrence.Kind != Phase0AMutationOccurrence || edge.Kind != Phase0AMutationEdge ||
			occurrence.Event != event.Event || occurrence.Occurrence.Slot != uint32(index+1) ||
			occurrence.Occurrence != edge.Occurrence || occurrence.Edge != edge.Edge ||
			sidecar.Occurrence != occurrence.Occurrence || sidecar.Edge != occurrence.Edge ||
			occurrence.Payload != event.Payload || occurrence.Predecessor == 0 ||
			occurrence.RolledBack || edge.RolledBack || sidecar.RolledBack {
			t.Fatalf("path %d occurrence=%+v edge=%+v sidecar=%+v", index, occurrence, edge, sidecar)
		}
		if _, ok := wantStates[sidecar.Boundary.State]; !ok || sidecar.Boundary.ByteOffset != 1 || sidecar.Boundary.Shifted {
			t.Fatalf("path %d boundary=%+v", index, sidecar.Boundary)
		}
		wantStates[sidecar.Boundary.State] = true
	}
	for state, seen := range wantStates {
		if !seen {
			t.Fatalf("missing reduction boundary state %d", state)
		}
	}
}

func TestPhase0AReductionFailureRetainsTombstonesAndClearsGrouping(t *testing.T) {
	core, head := newSharedReductionFixture(t, true)
	run := phase0ABeginShiftProvenanceRun(t, core, phase0AReductionLimits())
	core.limits.MaxNodes = uint32(len(core.nodes))
	if _, err := core.Reduce(head, 9, 0, ForkOrder{}); err == nil {
		t.Fatal("node-capped reduction succeeded")
	}
	proof, err := Phase0AObserverProof(core, run)
	if err != nil {
		t.Fatal(err)
	}
	if len(proof.Mutations) != 3 || len(proof.ReductionOccurrences) != 1 {
		t.Fatalf("failed proof mutations=%+v reduction=%+v", proof.Mutations, proof.ReductionOccurrences)
	}
	for _, mutation := range proof.Mutations {
		if !mutation.RolledBack || mutation.RollbackCause != Phase0ARollbackReturnedError {
			t.Fatalf("mutation was not retained as returned-error tombstone: %+v", mutation)
		}
	}
	if sidecar := proof.ReductionOccurrences[0]; !sidecar.RolledBack || sidecar.RollbackCause != Phase0ARollbackReturnedError {
		t.Fatalf("reduction sidecar was not tombstoned: %+v", sidecar)
	}
	core.limits.MaxNodes = 4096
	if _, err := core.Reduce(head, 9, 0, ForkOrder{}); err != nil {
		t.Fatalf("reduction grouping leaked after rollback: %v", err)
	}
	proof, err = Phase0AObserverProof(core, run)
	if err != nil {
		t.Fatal(err)
	}
	if len(proof.ReductionOccurrences) != 3 || proof.ReductionOccurrences[1].RolledBack || proof.ReductionOccurrences[2].RolledBack {
		t.Fatalf("post-rollback reduction proof=%+v", proof.ReductionOccurrences)
	}
}

func TestPhase0AReductionOuterPanicTombstonesCommittedInnerAttempt(t *testing.T) {
	core, head := newSharedReductionFixture(t, true)
	run := phase0ABeginShiftProvenanceRun(t, core, phase0AReductionLimits())
	reduceSucceeded := false
	panicObserved := false
	func() {
		defer func() {
			if recover() != nil {
				panicObserved = true
			}
		}()
		_ = core.ApplyAtomic(func() error {
			if _, err := core.Reduce(head, 9, 0, ForkOrder{}); err != nil {
				return err
			}
			reduceSucceeded = true
			panic("outer panic")
		})
	}()
	if !reduceSucceeded || !panicObserved {
		t.Fatalf("panic fixture reduce_succeeded=%t panic_observed=%t", reduceSucceeded, panicObserved)
	}
	proof, err := Phase0AObserverProof(core, run)
	if err != nil {
		t.Fatal(err)
	}
	if len(proof.Mutations) == 0 || len(proof.ReductionOccurrences) == 0 {
		t.Fatalf("outer panic produced no attempted proof rows: %+v", proof)
	}
	for _, mutation := range proof.Mutations {
		if !mutation.RolledBack || mutation.RollbackCause != Phase0ARollbackPanic {
			t.Fatalf("outer panic mutation=%+v", mutation)
		}
	}
	for _, sidecar := range proof.ReductionOccurrences {
		if !sidecar.RolledBack || sidecar.RollbackCause != Phase0ARollbackPanic {
			t.Fatalf("outer panic sidecar=%+v", sidecar)
		}
	}
}

func TestPhase0AReductionTrailingExtraMigrationProvesExactSource(t *testing.T) {
	core, err := New(diagnosticReduceTable(1, 3), Limits{MaxDerivations: 4, MaxPopPaths: 4})
	if err != nil {
		t.Fatal(err)
	}
	run := phase0ABeginShiftProvenanceRun(t, core, phase0AReductionLimits())
	seed, _ := core.Seed(1, 0)
	ordinary, _ := core.appendSubtree(subtreeRecord{symbol: 10, endByte: 1, terminal: true}, nil, nil, nil)
	extra, _ := core.appendSubtree(subtreeRecord{symbol: 11, startByte: 1, endByte: 2, extra: true, terminal: true}, nil, nil, nil)
	head := phase0AObservedTerminalPrivate(t, core, ordinary, seed.Node, 3, 1, false, 0, ForkOrder{})
	head = phase0AObservedTerminalPrivate(t, core, extra, head.Node, 3, 2, true, 0, ForkOrder{})
	frontier, parseErr := core.Reduce(head, 1, 0, ForkOrder{})
	if parseErr != nil || len(frontier) != 1 {
		t.Fatalf("diagnostic observer changed trailing-extra parse frontier=%v err=%v", frontier, parseErr)
	}
	proof, proofErr := Phase0AObserverProof(core, run)
	if proofErr != nil || proof.Failure != nil || len(proof.TrailingExtraMigrations) != 1 {
		t.Fatalf("trailing-extra proof err=%v proof=%+v", proofErr, proof)
	}
	migration := proof.TrailingExtraMigrations[0]
	if migration.SourceLink == 0 || migration.SourceLowerLink == 0 || migration.SourceExpression == 0 || migration.SourceOccurrence.Event != migration.Occurrence.Event || migration.Occurrence.Slot != migration.SourceOccurrence.Slot+1 || migration.SourceEdge == migration.Edge || migration.Payload != extra || migration.Predecessor == 0 || migration.RolledBack {
		t.Fatalf("trailing-extra migration=%+v", migration)
	}
}

func TestPhase0AReductionSchedulerPoisonClearsGrouping(t *testing.T) {
	core := phase0ATestCore(t)
	run := phase0ABeginShiftProvenanceRun(t, core, phase0AReductionLimits())
	err := core.ApplySchedulerAtomic(func(token SchedulerTransactionToken) error {
		phase0ABeginReductionConstruction(core, 1)
		if ownedErr := core.RunSchedulerOwned(token, func() error { return errors.New("owned failure") }); ownedErr == nil {
			t.Fatal("owned failure returned nil")
		}
		phase0AObservers.Lock()
		active := phase0AObservers.byCore[core].reduction.active
		phase0AObservers.Unlock()
		if active {
			t.Fatal("scheduler poison retained active reduction grouping")
		}
		return nil
	})
	if err == nil {
		t.Fatal("poisoned scheduler transaction committed")
	}
	proof, proofErr := Phase0AObserverProof(core, run)
	if proofErr != nil || proof.FirstPoison == nil || len(proof.Mutations) != 0 || len(proof.ReductionOccurrences) != 0 {
		t.Fatalf("scheduler poison proof=%+v err=%v", proof, proofErr)
	}
}

func TestPhase0AReductionEventScratchCapFailsBeforeRecords(t *testing.T) {
	core, head := newSharedReductionFixture(t, true)
	limits := phase0AReductionLimits()
	firstPathBytes := phase0AEventRecordBytes + phase0AOccurrenceRecordBytes + phase0AEdgeRecordBytes + phase0AReductionRecordBytes + phase0AReductionEventBytes
	limits.MaxBytes = phase0AFrameRecordBytes + phase0ASidecarFrameBytes + phase0ARouteFrameBytes + firstPathBytes - 1
	run := phase0ABeginShiftProvenanceRun(t, core, limits)
	frontier, parseErr := core.Reduce(head, 9, 0, ForkOrder{})
	if parseErr != nil || len(frontier) != 2 {
		t.Fatalf("diagnostic cap changed parse frontier=%v err=%v", frontier, parseErr)
	}
	proof, proofErr := Phase0AObserverProof(core, run)
	var typed *Phase0AError
	if !errors.As(proofErr, &typed) || typed.Kind != Phase0AErrorByteCap || len(proof.Mutations) != 0 || len(proof.ReductionOccurrences) != 0 || proof.OccurrenceCount != 0 || proof.OccurrenceBytes != 0 {
		t.Fatalf("pre-allocation cap proof=%+v err=%v", proof, proofErr)
	}
	phase0AObservers.Lock()
	observer := phase0AObservers.byCore[core]
	if observer.nextEvent != 0 || observer.nextEdge != 0 || phase0AObservers.records != 3 || phase0AObservers.bytes != phase0AFrameRecordBytes+phase0ASidecarFrameBytes+phase0ARouteFrameBytes {
		t.Errorf("cap advanced observer counters event=%d edge=%d records=%d bytes=%d", observer.nextEvent, observer.nextEdge, phase0AObservers.records, phase0AObservers.bytes)
	}
	phase0AObservers.Unlock()
}

func TestPhase0AReductionAccountingMatchesStoredRows(t *testing.T) {
	if phase0AReductionRecordBytes < 136 || phase0AReductionEventBytes < 48 {
		t.Fatalf("reduction accounting undercharges stored rows sidecar=%d event=%d", phase0AReductionRecordBytes, phase0AReductionEventBytes)
	}
}

func TestPhase0AReductionFinishCountMismatchFailsClosed(t *testing.T) {
	core := phase0ATestCore(t)
	run := phase0ABeginShiftProvenanceRun(t, core, phase0AReductionLimits())
	if err := core.ApplyAtomic(func() error {
		phase0ABeginReductionConstruction(core, 2)
		phase0AFinishReductionConstruction(core)
		return nil
	}); err != nil {
		t.Fatalf("diagnostic observer changed transaction result: %v", err)
	}
	proof, proofErr := Phase0AObserverProof(core, run)
	var typed *Phase0AError
	if !errors.As(proofErr, &typed) || typed.Kind != Phase0AErrorInvalidOccurrence || proof.Failure == nil || len(proof.Mutations) != 0 {
		t.Fatalf("finish mismatch proof=%+v err=%v", proof, proofErr)
	}
}
