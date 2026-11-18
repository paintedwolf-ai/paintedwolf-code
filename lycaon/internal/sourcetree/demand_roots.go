package sourcetree

import (
	"context"
	"maps"
	"path"
	"slices"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/pagedview"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
)

// ReplaceOnDemandRoots updates the appended roots while keeping project disclosures.
func (v *View) ReplaceOnDemandRoots(ctx context.Context, demand []Root) (bool, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.closed {
		return false, pagedview.ErrExpired
	}
	start := slices.IndexFunc(v.rootOrder, func(root Root) bool { return root.OnDemand })
	if start < 0 {
		start = len(v.rootOrder)
	}
	if slices.Equal(v.rootOrder[start:], demand) {
		return false, nil
	}
	current := v.roots
	roots := maps.Clone(current)
	for _, root := range v.rootOrder[start:] {
		delete(roots, root.ID)
	}
	for _, root := range demand {
		if _, exists := roots[root.ID]; !root.OnDemand || exists {
			return false, ErrAddress
		}
		roots[root.ID] = root.Root
	}
	for _, root := range v.rootOrder[start:] {
		if root.Root != roots[root.ID] {
			v.demandReleases[root.ID]()
			delete(v.demandReleases, root.ID)
		}
	}
	for _, root := range demand {
		if root.Root != current[root.ID] {
			v.holdDemandRoot(ctx, root)
		}
	}
	v.rootOrder = append(slices.Clone(v.rootOrder[:start]), demand...)
	v.roots = roots
	v.rules.limitToRoots(roots)
	v.revision = uuid.NewString()
	v.resetPreparationLocked()
	return true, nil
}

func (v *View) holdDemandRoot(ctx context.Context, root Root) {
	unsubscribe := v.catalog.Directories.SubscribeNavigation(root.Root, v.wakeLive)
	release, err := v.catalog.Trees.HoldRoot(ctx, v.scope.Project, root.Root)
	v.demandReleases[root.ID] = func() {
		unsubscribe()
		if err == nil {
			release()
		}
	}
}

// RefreshOnDemandDirectories reconciles only directories the person has opened.
func (v *View) RefreshOnDemandDirectories(rootID string, dirs []string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	root, ok := v.roots[rootID]
	if v.closed || !ok || !root.OnDemand {
		return
	}
	for _, dir := range dirs {
		if !validAddress(Address{Root: rootID, Path: dir}) {
			continue
		}
		for current := dir; ; current = path.Dir(current) {
			if !v.rules.At(Address{Root: rootID, Path: current}).Open {
				break
			}
			if current == "." {
				v.loadLocked(root, dir)
				break
			}
		}
	}
}
