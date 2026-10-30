//go:build linux

package launchguard

import (
	"bytes"
	"encoding/binary"
	"errors"
	"slices"
)

const (
	mountPlanHeaderBytes = 32
	maxMountPlanBytes    = mountPlanHeaderBytes + 2*maxMountTopologyBytes + 32*maxMountTopologyEntries
)

// The sealed stage plan carries observations, not authority inferred from a
// pathname, filesystem type or inode. Its producer owns namespace custody.
type moduleViewPlan struct {
	source     []byte
	staging    []byte
	structural []int
	initial    []initialMountLease
}

type initialMountLease struct {
	lease   uint64
	mountID int
	inode   uint64
}

func encodeModuleViewPlan(plan moduleViewPlan) ([]byte, error) {
	sourceSize, stagingSize := len(plan.source), len(plan.staging)
	structuralCount, initialCount := len(plan.structural), len(plan.initial)
	if sourceSize == 0 || sourceSize > maxMountTopologyBytes || stagingSize == 0 || stagingSize > maxMountTopologyBytes || structuralCount == 0 || structuralCount > maxMountTopologyEntries || initialCount > maxMountTopologyEntries {
		return nil, errors.New("module-view plan exceeds topology bounds")
	}
	plan.structural = slices.Clone(plan.structural)
	slices.Sort(plan.structural)
	if err := validateModuleViewPlan(plan); err != nil {
		return nil, err
	}
	size := mountPlanHeaderBytes + sourceSize + stagingSize + 8*structuralCount + 24*initialCount
	raw := make([]byte, size)
	copy(raw, "PWVIEW01")
	binary.LittleEndian.PutUint32(raw[8:], 1)
	binary.LittleEndian.PutUint32(raw[16:], uint32(sourceSize))
	binary.LittleEndian.PutUint32(raw[20:], uint32(stagingSize))
	binary.LittleEndian.PutUint32(raw[24:], uint32(structuralCount))
	binary.LittleEndian.PutUint32(raw[28:], uint32(initialCount))
	cursor := mountPlanHeaderBytes
	cursor += copy(raw[cursor:], plan.source)
	cursor += copy(raw[cursor:], plan.staging)
	for _, id := range plan.structural {
		if id <= 0 {
			return nil, errors.New("invalid structural mount identity")
		}
		binary.LittleEndian.PutUint64(raw[cursor:], uint64(id))
		cursor += 8
	}
	for _, item := range plan.initial {
		if item.mountID <= 0 {
			return nil, errors.New("invalid initial mount identity")
		}
		binary.LittleEndian.PutUint64(raw[cursor:], item.lease)
		binary.LittleEndian.PutUint64(raw[cursor+8:], uint64(item.mountID))
		binary.LittleEndian.PutUint64(raw[cursor+16:], item.inode)
		cursor += 24
	}
	return raw, nil
}

func decodeModuleViewPlan(raw []byte) (moduleViewPlan, error) {
	var plan moduleViewPlan
	if len(raw) < mountPlanHeaderBytes || len(raw) > maxMountPlanBytes || !bytes.Equal(raw[:8], []byte("PWVIEW01")) || binary.LittleEndian.Uint32(raw[8:]) != 1 || binary.LittleEndian.Uint32(raw[12:]) != 0 {
		return plan, errors.New("invalid module-view plan header")
	}
	sourceSize := uint64(binary.LittleEndian.Uint32(raw[16:]))
	stagingSize := uint64(binary.LittleEndian.Uint32(raw[20:]))
	structuralCount := uint64(binary.LittleEndian.Uint32(raw[24:]))
	initialCount := uint64(binary.LittleEndian.Uint32(raw[28:]))
	if sourceSize == 0 || sourceSize > maxMountTopologyBytes || stagingSize == 0 || stagingSize > maxMountTopologyBytes || structuralCount == 0 || structuralCount > maxMountTopologyEntries || initialCount > maxMountTopologyEntries {
		return plan, errors.New("module-view plan exceeds topology bounds")
	}
	// Bounded uint32 fields are widened before arithmetic or allocation.
	expected := uint64(mountPlanHeaderBytes) + sourceSize + stagingSize + 8*structuralCount + 24*initialCount
	if expected != uint64(len(raw)) {
		return plan, errors.New("truncated or trailing module-view plan")
	}
	cursor := mountPlanHeaderBytes
	plan.source = bytes.Clone(raw[cursor : cursor+int(sourceSize)])
	cursor += int(sourceSize)
	plan.staging = bytes.Clone(raw[cursor : cursor+int(stagingSize)])
	cursor += int(stagingSize)
	plan.structural = make([]int, int(structuralCount))
	maxInt := uint64(^uint(0) >> 1)
	var previous uint64
	for index := range plan.structural {
		id := binary.LittleEndian.Uint64(raw[cursor:])
		if id == 0 || id > maxInt || id <= previous {
			return moduleViewPlan{}, errors.New("noncanonical structural mount identities")
		}
		plan.structural[index] = int(id)
		previous = id
		cursor += 8
	}
	plan.initial = make([]initialMountLease, int(initialCount))
	for index := range plan.initial {
		id := binary.LittleEndian.Uint64(raw[cursor+8:])
		if id == 0 || id > maxInt {
			return moduleViewPlan{}, errors.New("invalid initial mount identity")
		}
		plan.initial[index] = initialMountLease{lease: binary.LittleEndian.Uint64(raw[cursor:]), mountID: int(id), inode: binary.LittleEndian.Uint64(raw[cursor+16:])}
		cursor += 24
	}
	if err := validateModuleViewPlan(plan); err != nil {
		return moduleViewPlan{}, err
	}
	return plan, nil
}

func moduleViewPlanWithinBounds(plan moduleViewPlan) bool {
	return len(plan.source) > 0 && len(plan.source) <= maxMountTopologyBytes && len(plan.staging) > 0 && len(plan.staging) <= maxMountTopologyBytes && len(plan.structural) > 0 && len(plan.structural) <= maxMountTopologyEntries && len(plan.initial) <= maxMountTopologyEntries
}

func validateModuleViewPlan(plan moduleViewPlan) error {
	if !moduleViewPlanWithinBounds(plan) {
		return errors.New("module-view plan exceeds topology bounds")
	}
	source, err := parseMountTopology(plan.source)
	if err != nil {
		return err
	}
	staging, err := parseMountTopology(plan.staging)
	if err != nil {
		return err
	}
	bySource := make(map[int]mountEntry, len(source))
	for _, entry := range source {
		bySource[entry.id] = entry
	}
	selected := make(map[int]bool, len(plan.structural))
	for _, id := range plan.structural {
		if _, exists := bySource[id]; !exists || selected[id] {
			return errors.New("structural mount identity is absent or repeated")
		}
		selected[id] = true
	}
	for _, entry := range source {
		_, parentExists := bySource[entry.parent]
		if (!parentExists && !selected[entry.id]) || (selected[entry.id] && parentExists && !selected[entry.parent]) {
			return errors.New("structural mount selection is not root- and ancestor-closed")
		}
	}
	byStaging := make(map[int]mountEntry, len(staging))
	for _, entry := range staging {
		byStaging[entry.id] = entry
	}
	seen := make(map[int]bool, len(plan.initial))
	aliases := make(map[string]bool, len(plan.initial))
	var previous uint64
	for _, item := range plan.initial {
		entry, exists := byStaging[item.mountID]
		if item.lease <= previous || item.inode == 0 || !exists || seen[item.mountID] || entry.mountPoint == "/" || aliases[entry.mountPoint] {
			return errors.New("invalid initial mount lease identity or alias")
		}
		previous = item.lease
		seen[item.mountID] = true
		aliases[entry.mountPoint] = true
	}
	return nil
}
