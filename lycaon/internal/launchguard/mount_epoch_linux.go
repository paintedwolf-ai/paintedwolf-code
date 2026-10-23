//go:build linux

package launchguard

import (
	"errors"
	"sort"
)

// Namespace cloning changes mount IDs and propagation membership. It does
// not change these kernel source facts; exact parent correspondence is checked
// separately, rather than interpreting a pathname prefix as mount ancestry.
type mountSourceKey struct {
	device, root, point, options string
	filesystem, source, super    string
}

func (entry mountEntry) sourceKey() mountSourceKey {
	return mountSourceKey{entry.device, entry.root, entry.mountPoint, entry.options, entry.filesystem, entry.source, entry.superOptions}
}

func correlateStructuralMounts(original []mountEntry, selected []int, current []mountEntry) (map[int]bool, error) {
	originalIDs := make(map[int]mountEntry, len(original))
	currentIDs := make(map[int]mountEntry, len(current))
	candidates := make(map[mountSourceKey]int, len(current))
	for _, entry := range original {
		originalIDs[entry.id] = entry
	}
	for _, entry := range current {
		currentIDs[entry.id] = entry
		key := entry.sourceKey()
		if _, exists := candidates[key]; exists {
			candidates[key] = 0
		} else {
			candidates[key] = entry.id
		}
	}
	mapped := make(map[int]int, len(selected))
	result := make(map[int]bool, len(selected))
	for _, id := range selected {
		entry, exists := originalIDs[id]
		if !exists || mapped[id] != 0 {
			return nil, errors.New("invalid selected structural source identity")
		}
		candidate := candidates[entry.sourceKey()]
		if candidate == 0 {
			return nil, errors.New("structural source is missing or ambiguous after namespace cloning")
		}
		mapped[id], result[candidate] = candidate, true
	}
	for _, id := range selected {
		before, after := originalIDs[id], currentIDs[mapped[id]]
		if _, parentExists := originalIDs[before.parent]; parentExists {
			if mapped[before.parent] == 0 || after.parent != mapped[before.parent] {
				return nil, errors.New("structural source parent correspondence changed")
			}
		} else if _, parentExists := currentIDs[after.parent]; parentExists {
			return nil, errors.New("structural source root correspondence changed")
		}
	}
	return result, nil
}

// One full epoch comparison surrounds the bounded preparation transaction.
// Known removals are explicit; an unexplained child, cover, rename or flag
// change refuses readiness even when each individually selected row survived.
func sameMountEpoch(before, after []mountEntry, removed map[int]bool) bool {
	remaining := make(map[int]mountEntry, len(before))
	for _, entry := range before {
		if !removed[entry.id] {
			remaining[entry.id] = entry
		}
	}
	if len(after) != len(remaining) {
		return false
	}
	for _, entry := range after {
		expected, exists := remaining[entry.id]
		if !exists || expected != entry {
			return false
		}
		delete(remaining, entry.id)
	}
	return len(remaining) == 0
}

// Detach descendants and covering mounts first. Depth is memoized once for
// the complete already-validated topology; a deep chain is not rescanned for
// every volume.
func volumeDetachOrder(entries []mountEntry, structural map[int]bool) []mountEntry {
	byID := make(map[int]mountEntry, len(entries))
	depth := make(map[int]int, len(entries))
	for _, entry := range entries {
		byID[entry.id] = entry
	}
	var volumes []mountEntry
	for _, entry := range entries {
		var chain []int
		current, exists := entry, true
		for exists && depth[current.id] == 0 {
			chain = append(chain, current.id)
			current, exists = byID[current.parent]
		}
		next := depth[current.id]
		for index := len(chain) - 1; index >= 0; index-- {
			next++
			depth[chain[index]] = next
		}
		if !structural[entry.id] {
			volumes = append(volumes, entry)
		}
	}
	sort.Slice(volumes, func(a, b int) bool {
		if depth[volumes[a].id] != depth[volumes[b].id] {
			return depth[volumes[a].id] > depth[volumes[b].id]
		}
		return volumes[a].id > volumes[b].id
	})
	return volumes
}
