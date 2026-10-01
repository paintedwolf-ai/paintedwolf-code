package parsercorephase0

import (
	"errors"
	"math"
	"math/bits"
)

const (
	boundaryIndexInitialCapacity = 16
	boundaryIndexLoadNumerator   = 7
	boundaryIndexLoadDenominator = 10
)

var errBoundaryIndexCapacity = errors.New("parser-core phase zero: canonical boundary index capacity")

// boundaryIndex is an exact, retained, frontier-local hash table. Slots from
// earlier frontiers remain allocated but become empty in O(1) by advancing the
// generation. The caller authenticates the full boundaryKey frontier before
// deriving the generation-local identity stored here.
type boundaryIndex struct {
	slots      []boundarySlot
	generation uint32
	count      uint32
	maxSlots   uint32
}

const boundaryIdentityShifted uint32 = 1

type boundaryIdentity struct {
	state      StateID
	byteOffset uint32
	checkpoint CheckpointID
	flags      uint32
}

type boundarySlot struct {
	key        boundaryIdentity
	id         NodeID
	generation uint32
}

// boundaryProbe is an ephemeral lookup result. It remains valid only until
// the next boundary-index mutation and is consumed synchronously by Seed or a
// single condensation operation.
type boundaryProbe struct {
	key   boundaryIdentity
	hash  uint64
	slot  uint32
	found bool
}

type boundaryIndexSnapshot struct {
	slots      []boundarySlot
	generation uint32
	count      uint32
}

// boundaryMutation names the backing array as well as the changed slot. A
// transaction may grow the index, so rollback must be able to restore writes
// made both before and after the rehash.
type boundaryMutation struct {
	slots    []boundarySlot
	index    uint32
	previous boundarySlot
}

func newBoundaryIndex(maxEntries uint32) (boundaryIndex, error) {
	if maxEntries == 0 {
		return boundaryIndex{}, errBoundaryIndexCapacity
	}
	required := (uint64(maxEntries)*boundaryIndexLoadDenominator + boundaryIndexLoadNumerator - 1) / boundaryIndexLoadNumerator
	if required < boundaryIndexInitialCapacity {
		required = boundaryIndexInitialCapacity
	}
	if required > uint64(math.MaxInt) {
		return boundaryIndex{}, errBoundaryIndexCapacity
	}
	maxSlots := uint64(1) << bits.Len64(required-1)
	if maxSlots > math.MaxUint32 || maxSlots > uint64(math.MaxInt) {
		return boundaryIndex{}, errBoundaryIndexCapacity
	}
	return boundaryIndex{generation: 1, maxSlots: uint32(maxSlots)}, nil
}

func (i *boundaryIndex) snapshot() boundaryIndexSnapshot {
	return boundaryIndexSnapshot{slots: i.slots, generation: i.generation, count: i.count}
}

func (i *boundaryIndex) restore(snapshot boundaryIndexSnapshot) {
	i.slots = snapshot.slots
	i.generation = snapshot.generation
	i.count = snapshot.count
}

func boundaryIdentityFromKey(key boundaryKey) boundaryIdentity {
	flags := uint32(0)
	if key.shifted {
		flags |= boundaryIdentityShifted
	}
	return boundaryIdentity{
		state: key.state, byteOffset: key.byteOffset,
		checkpoint: key.checkpoint, flags: flags,
	}
}

func (i *boundaryIndex) probe(key boundaryIdentity) (boundaryProbe, NodeID) {
	return i.probeWithHash(key, boundaryIdentityHash(key))
}

func (i *boundaryIndex) probeWithHash(key boundaryIdentity, hash uint64) (boundaryProbe, NodeID) {
	result := boundaryProbe{key: key, hash: hash}
	if len(i.slots) == 0 {
		return result, 0
	}
	mask := uint64(len(i.slots) - 1)
	start := result.hash & mask
	for offset := uint64(0); offset < uint64(len(i.slots)); offset++ {
		index := uint32((start + offset) & mask)
		slot := &i.slots[index]
		if slot.generation != i.generation {
			result.slot = index
			return result, 0
		}
		if slot.key == key {
			result.slot = index
			result.found = true
			return result, slot.id
		}
	}
	return result, 0
}

func (i *boundaryIndex) get(key boundaryKey) (NodeID, bool) {
	probe, id := i.probe(boundaryIdentityFromKey(key))
	return id, probe.found
}

func (i *boundaryIndex) publish(probe boundaryProbe, id NodeID, journal *[]boundaryMutation, transactional bool) error {
	if id == 0 {
		return errors.New("parser-core phase zero: zero canonical boundary node")
	}
	if len(i.slots) == 0 {
		if err := i.grow(boundaryIndexInitialCapacity); err != nil {
			return err
		}
		probe, _ = i.probeWithHash(probe.key, probe.hash)
	}
	if !probe.found && uint64(i.count+1)*boundaryIndexLoadDenominator > uint64(len(i.slots))*boundaryIndexLoadNumerator {
		if len(i.slots) >= int(i.maxSlots) {
			return errBoundaryIndexCapacity
		}
		if err := i.grow(len(i.slots) * 2); err != nil {
			return err
		}
		probe, _ = i.probeWithHash(probe.key, probe.hash)
	}
	if transactional {
		*journal = append(*journal, boundaryMutation{
			slots: i.slots, index: probe.slot, previous: i.slots[probe.slot],
		})
	}
	i.slots[probe.slot] = boundarySlot{key: probe.key, id: id, generation: i.generation}
	if !probe.found {
		i.count++
	}
	return nil
}

func (i *boundaryIndex) set(key boundaryKey, id NodeID, journal *[]boundaryMutation, transactional bool) error {
	probe, _ := i.probe(boundaryIdentityFromKey(key))
	return i.publish(probe, id, journal, transactional)
}

func (i *boundaryIndex) grow(capacity int) error {
	if capacity < boundaryIndexInitialCapacity {
		capacity = boundaryIndexInitialCapacity
	}
	if capacity > int(i.maxSlots) || capacity <= 0 || capacity&(capacity-1) != 0 {
		return errBoundaryIndexCapacity
	}
	previous := i.slots
	i.slots = make([]boundarySlot, capacity)
	for index := range previous {
		slot := previous[index]
		if slot.generation != i.generation {
			continue
		}
		probe, _ := i.probe(slot.key)
		if probe.found {
			return errors.New("parser-core phase zero: duplicate canonical boundary during rehash")
		}
		i.slots[probe.slot] = slot
	}
	return nil
}

func (i *boundaryIndex) advanceGeneration() {
	if i.generation == math.MaxUint32 {
		clear(i.slots)
		i.generation = 1
	} else {
		i.generation++
	}
	i.count = 0
}

func (i *boundaryIndex) reset() {
	i.advanceGeneration()
}

func (i *boundaryIndex) logicalMap() map[boundaryIdentity]NodeID {
	result := make(map[boundaryIdentity]NodeID, i.count)
	for index := range i.slots {
		slot := i.slots[index]
		if slot.generation == i.generation {
			result[slot.key] = slot.id
		}
	}
	return result
}

func boundaryIdentityHash(key boundaryIdentity) uint64 {
	// CheckpointID already names exact serialized scanner bytes. Complete key
	// equality still resolves every table-hash collision.
	h := (uint64(key.state) << 32) | uint64(key.byteOffset)
	h ^= bits.RotateLeft64(uint64(key.checkpoint), 17)
	if key.flags&boundaryIdentityShifted != 0 {
		h ^= 0xd6e8feb86659fd93
	}
	h ^= h >> 30
	h *= 0xbf58476d1ce4e5b9
	return h ^ (h >> 27)
}
