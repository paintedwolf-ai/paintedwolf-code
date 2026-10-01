package parsercorephase0

import (
	"errors"
	"math"
	"math/rand"
	"reflect"
	"testing"
)

func TestBoundaryIndexMatchesMapModelAcrossFrontiers(t *testing.T) {
	index, err := newBoundaryIndex(512)
	if err != nil {
		t.Fatal(err)
	}
	model := make(map[boundaryIdentity]NodeID)
	random := rand.New(rand.NewSource(0x69b0_0da7))
	frontier := uint64(1)
	for step := 0; step < 5000; step++ {
		if step != 0 && step%137 == 0 {
			index.advanceGeneration()
			clear(model)
			frontier++
		}
		checkpoint := CheckpointID(random.Uint32())
		key := boundaryKey{
			frontier: frontier, state: StateID(random.Intn(97)), byteOffset: uint32(random.Intn(211)),
			shifted: random.Intn(2) != 0, checkpoint: checkpoint,
		}
		id := NodeID(step + 1)
		if err := index.set(key, id, nil, false); err != nil {
			t.Fatalf("step %d set: %v", step, err)
		}
		model[boundaryIdentityFromKey(key)] = id
		if got, ok := index.get(key); !ok || got != id {
			t.Fatalf("step %d immediate lookup=(%d,%t), want (%d,true)", step, got, ok, id)
		}
		if got := index.logicalMap(); !reflect.DeepEqual(got, model) {
			t.Fatalf("step %d model mismatch: got=%v want=%v", step, got, model)
		}
	}
}

func TestBoundaryIndexFullKeyEqualitySurvivesHashCollision(t *testing.T) {
	index, err := newBoundaryIndex(64)
	if err != nil {
		t.Fatal(err)
	}
	if err := index.grow(boundaryIndexInitialCapacity); err != nil {
		t.Fatal(err)
	}
	first := boundaryKey{frontier: 1, state: 1, checkpoint: 1}
	var second boundaryKey
	wantBucket := boundaryIdentityHash(boundaryIdentityFromKey(first)) & uint64(len(index.slots)-1)
	for state := StateID(2); ; state++ {
		candidate := boundaryKey{frontier: 1, state: state, checkpoint: CheckpointID(state + 1)}
		if boundaryIdentityHash(boundaryIdentityFromKey(candidate))&uint64(len(index.slots)-1) == wantBucket {
			second = candidate
			break
		}
	}
	if err := index.set(first, 11, nil, false); err != nil {
		t.Fatal(err)
	}
	if err := index.set(second, 22, nil, false); err != nil {
		t.Fatal(err)
	}
	if got, ok := index.get(first); !ok || got != 11 {
		t.Fatalf("first collision lookup=(%d,%t)", got, ok)
	}
	if got, ok := index.get(second); !ok || got != 22 {
		t.Fatalf("second collision lookup=(%d,%t)", got, ok)
	}
}

func TestBoundaryProbePublishesAndReplacesWithoutRecounting(t *testing.T) {
	index, err := newBoundaryIndex(64)
	if err != nil {
		t.Fatal(err)
	}
	key := boundaryIdentity{state: 7, byteOffset: 11, checkpoint: 13, flags: boundaryIdentityShifted}
	probe, id := index.probe(key)
	if probe.found || id != 0 {
		t.Fatalf("empty probe=(%+v,%d)", probe, id)
	}
	if err := index.publish(probe, 17, nil, false); err != nil {
		t.Fatal(err)
	}
	found, id := index.probe(key)
	if !found.found || id != 17 || index.count != 1 {
		t.Fatalf("published probe=(%+v,%d) count=%d", found, id, index.count)
	}
	if err := index.publish(found, 19, nil, false); err != nil {
		t.Fatal(err)
	}
	replaced, id := index.probe(key)
	if !replaced.found || id != 19 || replaced.slot != found.slot || index.count != 1 {
		t.Fatalf("replacement probe=(%+v,%d) first=%+v count=%d", replaced, id, found, index.count)
	}
}

func TestBoundaryProbeReprobesAfterGrowth(t *testing.T) {
	index, err := newBoundaryIndex(64)
	if err != nil {
		t.Fatal(err)
	}
	for entry := 0; entry < 11; entry++ {
		key := boundaryKey{frontier: 1, state: StateID(entry + 1), byteOffset: uint32(entry * 3)}
		if err := index.set(key, NodeID(entry+1), nil, false); err != nil {
			t.Fatalf("entry %d: %v", entry, err)
		}
	}
	if len(index.slots) != boundaryIndexInitialCapacity {
		t.Fatalf("initial capacity=%d", len(index.slots))
	}
	key := boundaryIdentity{state: 99, byteOffset: 101, checkpoint: 3}
	probe, _ := index.probe(key)
	oldSlot := probe.slot
	if err := index.publish(probe, 99, nil, false); err != nil {
		t.Fatal(err)
	}
	if len(index.slots) != boundaryIndexInitialCapacity*2 || index.count != 12 {
		t.Fatalf("grown capacity=%d count=%d", len(index.slots), index.count)
	}
	found, id := index.probe(key)
	if !found.found || id != 99 {
		t.Fatalf("grown lookup=(%+v,%d), old slot=%d", found, id, oldSlot)
	}
}

func TestWriteBoundaryRejectsStaleFrontierWithoutMutation(t *testing.T) {
	compact, err := New(&fakeTable{}, Limits{MaxNodes: 16})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := compact.Seed(1, 0); err != nil {
		t.Fatal(err)
	}
	stale := compact.boundaryKey(2, 1)
	if err := compact.BeginFrontier(); err != nil {
		t.Fatal(err)
	}
	before := compact.boundaries.logicalMap()
	if err := compact.writeBoundary(stale, 2); err == nil {
		t.Fatal("stale boundary write succeeded")
	}
	if got := compact.boundaries.logicalMap(); !reflect.DeepEqual(got, before) {
		t.Fatalf("stale boundary write mutated index: got=%v want=%v", got, before)
	}
}

func TestBoundaryIndexCapacityFailureIsAtomic(t *testing.T) {
	index, err := newBoundaryIndex(1)
	if err != nil {
		t.Fatal(err)
	}
	for entry := 0; entry < 11; entry++ {
		key := boundaryKey{frontier: 1, state: StateID(entry + 1)}
		if err := index.set(key, NodeID(entry+1), nil, false); err != nil {
			t.Fatalf("entry %d: %v", entry, err)
		}
	}
	before := index.logicalMap()
	err = index.set(boundaryKey{frontier: 1, state: 99}, 99, nil, false)
	if !errors.Is(err, errBoundaryIndexCapacity) {
		t.Fatalf("capacity error=%v", err)
	}
	if got := index.logicalMap(); !reflect.DeepEqual(got, before) {
		t.Fatalf("capacity failure mutated index: got=%v want=%v", got, before)
	}
}

func TestBoundaryIndexGrowthRollbackRestoresBackingAndIDs(t *testing.T) {
	compact, err := New(&fakeTable{}, Limits{MaxNodes: 64})
	if err != nil {
		t.Fatal(err)
	}
	seed, err := compact.Seed(1, 0)
	if err != nil {
		t.Fatal(err)
	}
	before := compact.boundaries.snapshot()
	beforeMap := compact.boundaries.logicalMap()
	sentinel := errors.New("rollback")
	err = compact.ApplyAtomic(func() error {
		for entry := 0; entry < 10; entry++ {
			if err := compact.writeBoundary(boundaryKey{frontier: 1, state: StateID(entry + 2)}, NodeID(entry+2)); err != nil {
				return err
			}
		}
		if err := compact.ApplyAtomic(func() error {
			return compact.writeBoundary(boundaryKey{frontier: 1, state: 100}, 100)
		}); err != nil {
			return err
		}
		if len(compact.boundaries.slots) != 32 {
			t.Fatalf("grown capacity=%d, want 32", len(compact.boundaries.slots))
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("rollback error=%v", err)
	}
	if len(compact.boundaries.slots) != len(before.slots) || compact.boundaries.generation != before.generation || compact.boundaries.count != before.count {
		t.Fatalf("index snapshot not restored: got=(%d,%d,%d) want=(%d,%d,%d)", len(compact.boundaries.slots), compact.boundaries.generation, compact.boundaries.count, len(before.slots), before.generation, before.count)
	}
	if got := compact.boundaries.logicalMap(); !reflect.DeepEqual(got, beforeMap) {
		t.Fatalf("rollback map=%v want=%v", got, beforeMap)
	}
	if got, ok := compact.boundaries.get(compact.boundaryKey(1, 0)); !ok || got != seed.Node {
		t.Fatalf("seed after rollback=(%d,%t), want (%d,true)", got, ok, seed.Node)
	}
}

func TestBoundaryIndexGenerationRolloverClearsStaleSlots(t *testing.T) {
	index, err := newBoundaryIndex(16)
	if err != nil {
		t.Fatal(err)
	}
	key := boundaryKey{frontier: 1, state: 1}
	if err := index.set(key, 1, nil, false); err != nil {
		t.Fatal(err)
	}
	index.generation = math.MaxUint32
	for slot := range index.slots {
		if index.slots[slot].id != 0 {
			index.slots[slot].generation = math.MaxUint32
		}
	}
	index.advanceGeneration()
	if index.generation != 1 || index.count != 0 {
		t.Fatalf("rollover generation=%d count=%d", index.generation, index.count)
	}
	if _, ok := index.get(key); ok {
		t.Fatal("pre-rollover key survived")
	}
}
