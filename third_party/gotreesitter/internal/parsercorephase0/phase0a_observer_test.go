//go:build gts_parsercorephase0 && gts_parsercorephase0a

package parsercorephase0

import (
	"errors"
	"math"
	"reflect"
	"sync"
	"testing"
)

func phase0ATestSession(t *testing.T, limits Phase0AObserverLimits) {
	t.Helper()
	if err := BeginPhase0AObserverSession(limits); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		phase0AObservers.Lock()
		for _, observer := range phase0AObservers.byCore {
			observer.active = false
		}
		phase0AObservers.Unlock()
		if err := EndPhase0AObserverSession(); err != nil {
			t.Errorf("end session: %v", err)
		}
	})
}

func phase0ATestCore(t *testing.T) *Core {
	t.Helper()
	c, err := New(&fakeTable{}, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestPhase0ACompositeIdentityAndAttemptStatus(t *testing.T) {
	phase0ATestSession(t, Phase0AObserverLimits{MaxCores: 2, MaxRecords: 8, MaxBytes: 1024})
	a, b := phase0ATestCore(t), phase0ATestCore(t)
	ar, err := a.BeginRun()
	if err != nil {
		t.Fatal(err)
	}
	br, err := b.BeginRun()
	if err != nil {
		t.Fatal(err)
	}
	if ar.CoreInstance == br.CoreInstance {
		t.Fatalf("core instances collide: %+v %+v", ar, br)
	}
	if _, err := a.BeginRun(); !isPhase0AError(err, Phase0AErrorRunActive, "") {
		t.Fatalf("active run error = %v", err)
	}
	attempt, err := phase0AAttempt(a, ar, 1)
	if err != nil || attempt.AttemptEpoch != 1 {
		t.Fatalf("attempt = %+v, %v", attempt, err)
	}
	event, err := phase0ANextConstructionEvent(a, ar)
	if err != nil {
		t.Fatal(err)
	}
	edge, err := phase0ANextScaffoldEdge(a, event)
	if err != nil {
		t.Fatal(err)
	}
	raw := ConstructionOccurrenceKey{Event: event, Slot: 7}
	if edge.Event != raw.Event || raw.Slot != 7 {
		t.Fatalf("composite keys lost: edge=%+v raw=%+v", edge, raw)
	}
	proof, err := Phase0AObserverProof(a, ar)
	if err != nil || len(proof.Mutations) != 2 || proof.Mutations[1].Kind != Phase0AMutationScaffoldEdge || proof.Mutations[1].Occurrence != (ConstructionOccurrenceKey{}) {
		t.Fatalf("scaffold edge leaked semantic occurrence proof: %+v err=%v", proof.Mutations, err)
	}
	if _, err := phase0AAttempt(a, ar, 2); !isPhase0AError(err, Phase0AErrorAttemptUnproven, "") {
		t.Fatalf("retry error = %v", err)
	}
	if err := a.EndRun(br); !isPhase0AError(err, Phase0AErrorStaleNamespace, "") {
		t.Fatalf("wrong namespace end = %v", err)
	}
}

func TestPhase0ASessionLifecycleAndPooledCore(t *testing.T) {
	limits := Phase0AObserverLimits{MaxCores: 1, MaxRecords: 2, MaxBytes: 128}
	if err := BeginPhase0AObserverSession(limits); err != nil {
		t.Fatal(err)
	}
	if err := BeginPhase0AObserverSession(limits); !isPhase0AError(err, Phase0AErrorSessionActive, "") {
		t.Fatalf("collision = %v", err)
	}
	c := phase0ATestCore(t)
	old, err := c.BeginRun()
	if err != nil {
		t.Fatal(err)
	}
	if err := EndPhase0AObserverSession(); !isPhase0AError(err, Phase0AErrorSessionHasRuns, "") {
		t.Fatalf("active end = %v", err)
	}
	if err := c.EndRun(old); err != nil {
		t.Fatal(err)
	}
	if err := EndPhase0AObserverSession(); err != nil {
		t.Fatal(err)
	}
	if phase0AObservers.byCore != nil {
		t.Fatal("pointer registry retained after session")
	}
	if err := BeginPhase0AObserverSession(limits); err != nil {
		t.Fatal(err)
	}
	newRun, err := c.BeginRun()
	if err != nil {
		t.Fatal(err)
	}
	if newRun.CoreInstance <= old.CoreInstance {
		t.Fatalf("core serial reused: old=%+v new=%+v", old, newRun)
	}
	if _, err := phase0ANextConstructionEvent(c, old); !isPhase0AError(err, Phase0AErrorStaleNamespace, "") {
		t.Fatalf("cross-session stale = %v", err)
	}
	if err := c.EndRun(newRun); err != nil {
		t.Fatal(err)
	}
	if err := EndPhase0AObserverSession(); err != nil {
		t.Fatal(err)
	}
}

func TestPhase0ACapsChargeAtomically(t *testing.T) {
	phase0ATestSession(t, Phase0AObserverLimits{MaxCores: 1, MaxRecords: 1, MaxBytes: phase0AEventRecordBytes})
	c := phase0ATestCore(t)
	run, _ := c.BeginRun()
	event, err := phase0ANextConstructionEvent(c, run)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := phase0ANextScaffoldEdge(c, event); !isPhase0AError(err, Phase0AErrorRecordCap, "") {
		t.Fatalf("record cap = %v", err)
	}
	phase0AObservers.Lock()
	observer := phase0AObservers.byCore[c]
	if observer.nextEdge != 0 || phase0AObservers.records != 1 || phase0AObservers.bytes != phase0AEventRecordBytes {
		t.Fatalf("failed charge mutated state")
	}
	phase0AObservers.Unlock()
	other := phase0ATestCore(t)
	if _, err := other.BeginRun(); !isPhase0AError(err, Phase0AErrorCoreCap, "") {
		t.Fatalf("core cap = %v", err)
	}
}

func TestPhase0AByteAndCoreSerialOverflow(t *testing.T) {
	phase0ATestSession(t, Phase0AObserverLimits{MaxCores: 1, MaxRecords: 2, MaxBytes: phase0AEventRecordBytes - 1})
	c := phase0ATestCore(t)
	run, _ := c.BeginRun()
	if _, err := phase0ANextConstructionEvent(c, run); !isPhase0AError(err, Phase0AErrorByteCap, "") {
		t.Fatalf("byte cap = %v", err)
	}
	if err := c.EndRun(run); !isPhase0AError(err, Phase0AErrorByteCap, "") {
		t.Fatalf("end run failure = %v", err)
	}
	phase0AObservers.Lock()
	delete(phase0AObservers.byCore, c)
	prior := phase0AObservers.nextCoreInstance
	phase0AObservers.nextCoreInstance = math.MaxUint64
	phase0AObservers.Unlock()
	if _, err := c.BeginRun(); !isPhase0AError(err, Phase0AErrorCounterOverflow, Phase0ACounterCoreInstance) {
		t.Fatalf("core overflow = %v", err)
	}
	phase0AObservers.Lock()
	phase0AObservers.nextCoreInstance = prior
	phase0AObservers.Unlock()
}

func TestPhase0AResetSerialsAndOverflow(t *testing.T) {
	phase0ATestSession(t, Phase0AObserverLimits{MaxCores: 1, MaxRecords: 16, MaxBytes: 2048})
	c := phase0ATestCore(t)
	firstRun, err := c.BeginRun()
	if err != nil {
		t.Fatal(err)
	}
	firstEvent, err := phase0ANextConstructionEvent(c, firstRun)
	if err != nil {
		t.Fatal(err)
	}
	secondEvent, err := phase0ANextConstructionEvent(c, firstRun)
	if err != nil {
		t.Fatal(err)
	}
	firstEdge, err := phase0ANextScaffoldEdge(c, firstEvent)
	if err != nil {
		t.Fatal(err)
	}
	secondEdge, err := phase0ANextScaffoldEdge(c, secondEvent)
	if err != nil {
		t.Fatal(err)
	}
	if firstEvent.Serial != 1 || secondEvent.Serial != 2 || firstEdge.Serial != 1 || secondEdge.Serial != 2 {
		t.Fatalf("serials rewound: events=(%d,%d) edges=(%d,%d)", firstEvent.Serial, secondEvent.Serial, firstEdge.Serial, secondEdge.Serial)
	}
	phase0AObservers.Lock()
	observer := phase0AObservers.byCore[c]
	observer.nextEvent = math.MaxUint64
	phase0AObservers.Unlock()
	if _, err := phase0ANextConstructionEvent(c, firstRun); !isPhase0AError(err, Phase0AErrorCounterOverflow, Phase0ACounterEventSerial) {
		t.Fatalf("event overflow = %v", err)
	}
	phase0AObservers.Lock()
	observer.nextEdge = math.MaxUint64
	phase0AObservers.Unlock()
	if _, err := phase0ANextScaffoldEdge(c, firstEvent); !isPhase0AError(err, Phase0AErrorCounterOverflow, Phase0ACounterEventSerial) {
		t.Fatalf("sticky event overflow = %v", err)
	}
	if err := c.Reset(); err != nil {
		t.Fatal(err)
	}
	phase0AObservers.Lock()
	resetObserver := phase0AObservers.byCore[c]
	if resetObserver.active || resetObserver.frames != nil || resetObserver.mutations != nil || resetObserver.eventIndex != nil || resetObserver.edgeIndex != nil || resetObserver.occurrenceIndex != nil || resetObserver.occurrenceCount != 0 || resetObserver.occurrenceBytes != 0 || resetObserver.firstPoison != nil || resetObserver.failure != nil {
		phase0AObservers.Unlock()
		t.Fatalf("reset retained observer capacity or mappings: %+v", resetObserver)
	}
	phase0AObservers.Unlock()
	if _, err := phase0ANextConstructionEvent(c, firstRun); !isPhase0AError(err, Phase0AErrorStaleNamespace, "") {
		t.Fatalf("reset namespace = %v", err)
	}
	secondRun, err := c.BeginRun()
	if err != nil {
		t.Fatal(err)
	}
	if secondRun.CoreInstance != firstRun.CoreInstance || secondRun.RunGeneration != firstRun.RunGeneration+1 {
		t.Fatalf("post-reset run = %+v after %+v", secondRun, firstRun)
	}
}

func TestPhase0AConcurrentAllocationUniqueAndSessionClean(t *testing.T) {
	const (
		coreCount    = 4
		runsPerCore  = 8
		eventsPerRun = 4
	)
	records := uint64(coreCount * runsPerCore * eventsPerRun * 2)
	limits := Phase0AObserverLimits{
		MaxCores:   coreCount,
		MaxRecords: records,
		MaxBytes:   uint64(coreCount*runsPerCore*eventsPerRun) * (phase0AEventRecordBytes + phase0AEdgeRecordBytes),
	}
	if err := BeginPhase0AObserverSession(limits); err != nil {
		t.Fatal(err)
	}
	ended := false
	t.Cleanup(func() {
		if ended {
			return
		}
		phase0AObservers.Lock()
		for _, observer := range phase0AObservers.byCore {
			observer.active = false
		}
		phase0AObservers.Unlock()
		_ = EndPhase0AObserverSession()
	})

	type allocation struct {
		event ConstructionEventKey
		edge  IncomingEdgeKey
	}
	allocations := make(chan allocation, records/2)
	errs := make(chan error, coreCount)
	var workers sync.WaitGroup
	for range coreCount {
		core := phase0ATestCore(t)
		workers.Add(1)
		go func() {
			defer workers.Done()
			for range runsPerCore {
				run, err := core.BeginRun()
				if err != nil {
					errs <- err
					return
				}
				for range eventsPerRun {
					event, err := phase0ANextConstructionEvent(core, run)
					if err != nil {
						errs <- err
						return
					}
					edge, err := phase0ANextScaffoldEdge(core, event)
					if err != nil {
						errs <- err
						return
					}
					allocations <- allocation{event: event, edge: edge}
				}
				if err := core.EndRun(run); err != nil {
					errs <- err
					return
				}
			}
		}()
	}
	workers.Wait()
	close(allocations)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	events := make(map[ConstructionEventKey]struct{}, records/2)
	edges := make(map[IncomingEdgeKey]struct{}, records/2)
	for allocation := range allocations {
		if _, exists := events[allocation.event]; exists {
			t.Fatalf("duplicate event key: %+v", allocation.event)
		}
		if _, exists := edges[allocation.edge]; exists {
			t.Fatalf("duplicate edge key: %+v", allocation.edge)
		}
		events[allocation.event] = struct{}{}
		edges[allocation.edge] = struct{}{}
	}
	want := coreCount * runsPerCore * eventsPerRun
	if len(events) != want || len(edges) != want {
		t.Fatalf("allocation counts events=%d edges=%d want=%d", len(events), len(edges), want)
	}
	if err := EndPhase0AObserverSession(); err != nil {
		t.Fatal(err)
	}
	ended = true
	if phase0AObservers.byCore != nil {
		t.Fatal("session retained core pointer registry")
	}
}

func TestPhase0ARunGenerationOverflowDoesNotMutate(t *testing.T) {
	phase0ATestSession(t, Phase0AObserverLimits{MaxCores: 1, MaxRecords: 1, MaxBytes: phase0AEventRecordBytes})
	core := phase0ATestCore(t)
	run, err := core.BeginRun()
	if err != nil {
		t.Fatal(err)
	}
	if err := core.EndRun(run); err != nil {
		t.Fatal(err)
	}
	phase0AObservers.Lock()
	observer := phase0AObservers.byCore[core]
	observer.runGeneration = math.MaxUint64
	before := *observer
	phase0AObservers.Unlock()
	if _, err := core.BeginRun(); !isPhase0AError(err, Phase0AErrorCounterOverflow, Phase0ACounterRunGeneration) {
		t.Fatalf("run generation overflow = %v", err)
	}
	phase0AObservers.Lock()
	after := *phase0AObservers.byCore[core]
	phase0AObservers.Unlock()
	if after.failure == nil || after.failure.Kind != Phase0AErrorCounterOverflow || after.failure.Counter != Phase0ACounterRunGeneration {
		t.Fatalf("overflow failure = %+v", after.failure)
	}
	after.failure = nil
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("overflow mutated observer: before=%+v after=%+v", before, after)
	}
}

func TestPhase0AEndThenBeginAdvancesAndStalesNamespace(t *testing.T) {
	phase0ATestSession(t, Phase0AObserverLimits{MaxCores: 1, MaxRecords: 2, MaxBytes: 2 * phase0AEventRecordBytes})
	core := phase0ATestCore(t)
	oldRun, err := core.BeginRun()
	if err != nil {
		t.Fatal(err)
	}
	if err := core.EndRun(oldRun); err != nil {
		t.Fatal(err)
	}
	newRun, err := core.BeginRun()
	if err != nil {
		t.Fatal(err)
	}
	if newRun.CoreInstance != oldRun.CoreInstance || newRun.RunGeneration != oldRun.RunGeneration+1 {
		t.Fatalf("new run = %+v after %+v", newRun, oldRun)
	}
	if _, err := phase0ANextConstructionEvent(core, oldRun); !isPhase0AError(err, Phase0AErrorStaleNamespace, "") {
		t.Fatalf("old namespace after EndRun/BeginRun = %v", err)
	}
}

func isPhase0AError(err error, kind Phase0AErrorKind, counter Phase0ACounter) bool {
	var proofErr *Phase0AError
	return errors.As(err, &proofErr) && proofErr.Kind == kind && proofErr.Counter == counter
}
