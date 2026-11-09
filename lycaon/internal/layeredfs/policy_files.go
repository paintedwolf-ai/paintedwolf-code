package layeredfs

import (
	"path"
	"sort"
	"strings"
)

// PolicyFileInventory describes the policy-file delta recorded in a layered
// view's retained parent layers and view-private state for one root, with
// explicit completeness, state generation, and OS directory-read accounting.
type PolicyFileInventory struct {
	Files      []string
	Removed    []string
	Complete   bool
	Generation uint64
	ReadDirs   int64
}

// PolicyInventory returns the policy-file inventory of rootID from the view's
// retained parent layers and view-private state without scanning the lower host
// trunk (ReadDirs == 0, Complete == false).
func (c *Core) PolicyInventory(rootID string, names ...string) PolicyFileInventory {
	if c == nil || rootID == "" {
		return PolicyFileInventory{}
	}
	targets := canonicalPolicyNames(names)
	if len(targets) == 0 {
		return PolicyFileInventory{}
	}
	c.mu.RLock()
	if c.state.released {
		c.mu.RUnlock()
		return PolicyFileInventory{}
	}
	_, ok := c.roots.byID[rootID]
	parentLayers := c.cfg.ParentLayers
	c.mu.RUnlock()
	if !ok {
		return PolicyFileInventory{}
	}

	active := make(map[string]bool)
	removed := make(map[string]bool)

	for i := len(parentLayers) - 1; i >= 0; i-- {
		snap, ok := parentLayers[i].(parentLayerSnapshotter)
		if !ok {
			continue
		}
		entries := snap.EntriesForRoot(rootID)
		for rel, entry := range entries {
			if entry.Kind == EntryAbsent || entry.Kind != EntryDirectory {
				removePolicyPathsUnder(active, removed, rel)
			}
			if entry.Kind != EntryRegular && foldsToAnyPolicyName(path.Base(rel), targets) {
				delete(active, rel)
				removed[rel] = true
			}
		}
		for rel, entry := range entries {
			if entry.Kind == EntryRegular && foldsToAnyPolicyName(path.Base(rel), targets) {
				active[rel] = true
				delete(removed, rel)
			}
		}
	}

	c.mu.RLock()
	if c.state.released {
		c.mu.RUnlock()
		return PolicyFileInventory{}
	}
	gen := uint64(1) + c.state.nsGen + c.state.seq.nextSeq + c.state.seq.nextGen
	for qp := range c.state.deletedPaths {
		if qp.RootID != rootID || qp.Rel == "." || qp.Rel == "" {
			continue
		}
		removePolicyPathsUnder(active, removed, qp.Rel)
		if foldsToAnyPolicyName(path.Base(qp.Rel), targets) {
			delete(active, qp.Rel)
			removed[qp.Rel] = true
		}
	}
	for qp, nid := range c.state.pathIndex {
		if qp.RootID != rootID || qp.Rel == "." || qp.Rel == "" {
			continue
		}
		node := c.state.nodesByID[nid]
		if node == nil || node.flags.unlinked {
			if foldsToAnyPolicyName(path.Base(qp.Rel), targets) {
				delete(active, qp.Rel)
				removed[qp.Rel] = true
			}
			continue
		}
		if node.kind != EntryDirectory {
			removePolicyPathsUnder(active, removed, qp.Rel)
		}
		if node.kind != EntryRegular && foldsToAnyPolicyName(path.Base(qp.Rel), targets) {
			delete(active, qp.Rel)
			removed[qp.Rel] = true
		}
	}
	for qp, nid := range c.state.pathIndex {
		if qp.RootID != rootID || qp.Rel == "." || qp.Rel == "" {
			continue
		}
		node := c.state.nodesByID[nid]
		if node != nil && !node.flags.unlinked && node.kind == EntryRegular && foldsToAnyPolicyName(path.Base(qp.Rel), targets) {
			active[qp.Rel] = true
			delete(removed, qp.Rel)
		}
	}
	c.mu.RUnlock()

	return PolicyFileInventory{
		Files:      sortedNames(active),
		Removed:    sortedNames(removed),
		Complete:   false,
		Generation: gen,
		ReadDirs:   0,
	}
}

// PolicyFiles returns the sorted root-relative paths of regular policy files
// recorded in the view's retained parent layers and view-private state.
func (c *Core) PolicyFiles(rootID string, names ...string) []string {
	return c.PolicyInventory(rootID, names...).Files
}

func canonicalPolicyNames(names []string) []string {
	seen := make(map[string]bool, len(names))
	var out []string
	for _, n := range names {
		n = strings.ToLower(strings.TrimSpace(n))
		if n != "" && !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

func foldsToAnyPolicyName(name string, targets []string) bool {
	for _, target := range targets {
		if strings.EqualFold(name, target) {
			return true
		}
	}
	return false
}

func removePolicyPathsUnder(active, removed map[string]bool, dirRel string) {
	prefix := dirRel + "/"
	for p := range active {
		if strings.HasPrefix(p, prefix) {
			delete(active, p)
			removed[p] = true
		}
	}
}
