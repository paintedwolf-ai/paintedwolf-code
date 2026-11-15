package consumer

import (
	"context"
	"fmt"
	"os"
	"path"
	"strings"

	"github.com/lycaon/lycaon/internal/layeredfs"
)

// BuildOrientationInventory builds the model-request orientation inventory for selectedRootIDs
// by overlaying O(changed) private changes over the existing source index without enumerating
// unrelated roots or walking the lower filesystem tree.
func (e *Engine) BuildOrientationInventory(ctx context.Context, selectedRootIDs []string) (OrientationInventory, error) {
	roots, allowed, err := e.resolveRequestedRoots(selectedRootIDs)
	if err != nil {
		return OrientationInventory{}, err
	}
	var idxRes SourceIndexResult
	if e.index != nil {
		idxRes, err = e.index.QueryIndex(ctx, SourceIndexQuery{RootIDs: roots})
		if err != nil {
			return OrientationInventory{}, err
		}
	} else {
		idxRes = SourceIndexResult{Coverage: CoverageAbsent}
	}

	delta, err := e.collectChangeDelta("", allowed, "")
	if err != nil {
		return OrientationInventory{}, err
	}
	entries, _ := e.mergeIndexAndDelta(idxRes, delta, allowed, "", 0)
	cov := idxRes.Coverage
	if cov == "" {
		cov = CoverageComplete
	}
	return OrientationInventory{
		ViewID:          e.viewID,
		SelectedRoots:   roots,
		Entries:         entries,
		PrivateChanges:  delta.divergence,
		Coverage:        cov,
		ObservedAt:      effectiveObservedAt(idxRes.ObservedAt),
		VisibilityFacts: e.VisibilityFacts(),
	}, nil
}

// SurveyBranch returns the merged root-qualified branch inventory from the source index overlaid
// with either the live worker mutation index or an immutable checkpoint manifest.
func (e *Engine) SurveyBranch(ctx context.Context, opts SurveyOptions) (SurveyResult, error) {
	roots, allowed, err := e.resolveRequestedRoots(opts.RootIDs)
	if err != nil {
		return SurveyResult{}, err
	}
	prefix := strings.Trim(strings.TrimSpace(opts.PathPrefix), "/")
	var idxRes SourceIndexResult
	if e.index != nil {
		idxRes, err = e.index.QueryIndex(ctx, SourceIndexQuery{
			RootIDs:    roots,
			Subroots:   opts.Subroots,
			PathPrefix: prefix,
			MaxResults: opts.MaxEntries,
		})
		if err != nil {
			return SurveyResult{}, err
		}
	} else {
		idxRes = SourceIndexResult{Coverage: CoverageAbsent}
	}

	ckptID := strings.TrimSpace(opts.CheckpointID)
	delta, err := e.collectChangeDelta(ckptID, allowed, prefix)
	if err != nil {
		return SurveyResult{}, err
	}
	entries, truncated := e.mergeIndexAndDelta(idxRes, delta, allowed, prefix, opts.MaxEntries)
	cov := idxRes.Coverage
	if cov == "" {
		cov = CoverageComplete
	}
	return SurveyResult{
		ViewID:          e.viewID,
		CheckpointID:    ckptID,
		Entries:         entries,
		Coverage:        cov,
		ObservedAt:      effectiveObservedAt(idxRes.ObservedAt),
		Truncated:       truncated,
		Source:          delta.source,
		VisibilityFacts: e.VisibilityFacts(),
	}, nil
}

// ListDirectory enumerates only the single requested directory through the live worker mount
// (or as of checkpointID when non-empty), reflecting private create, delete, rename, and
// directory replacement without walking sibling or descendant subtrees.
func (e *Engine) ListDirectory(ctx context.Context, qpath layeredfs.QualifiedPath, checkpointID string) ([]InventoryEntry, error) {
	nq, err := normalizeQualifiedPath(qpath)
	if err != nil {
		return nil, err
	}
	if _, ok := e.rootsByID[nq.RootID]; !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownRoot, nq.RootID)
	}
	ckptID := strings.TrimSpace(checkpointID)
	if ckptID != "" {
		return e.listCheckpointDirectory(ctx, nq, ckptID)
	}
	return e.listLiveDirectory(nq)
}

func (e *Engine) listLiveDirectory(nq layeredfs.QualifiedPath) ([]InventoryEntry, error) {
	if e.core != nil {
		dirEntries, err := e.core.ReadDir(nq)
		if err != nil {
			if e.tracker != nil {
				e.tracker.ObserveLookupResult(nq, layeredfs.EntryAttr{}, err)
			}
			return nil, err
		}
		out := make([]InventoryEntry, 0, len(dirEntries))
		for _, de := range dirEntries {
			childRel := de.Name
			if nq.Rel != "." {
				childRel = nq.Rel + "/" + de.Name
			}
			if IsInternalStoragePath(childRel) {
				continue
			}
			childQP := layeredfs.QualifiedPath{RootID: nq.RootID, Rel: childRel}
			var linkTarget string
			if de.Kind == layeredfs.EntrySymlink {
				linkTarget, _ = e.core.Readlink(childQP)
			}
			out = append(out, e.attrToInventoryEntry(childQP, layeredfs.EntryAttr{
				Layer: de.Layer, Kind: de.Kind, Mode: de.Mode, Size: de.Size, ModTime: de.ModTime, LinkTarget: linkTarget,
			}))
		}
		sortInventoryEntries(out)
		return out, nil
	}

	absDir, err := e.ResolveMountedPath(nq)
	if err != nil {
		return nil, err
	}
	osEntries, err := os.ReadDir(absDir)
	if err != nil {
		return nil, err
	}
	out := make([]InventoryEntry, 0, len(osEntries))
	for _, de := range osEntries {
		childRel := de.Name()
		if nq.Rel != "." {
			childRel = nq.Rel + "/" + de.Name()
		}
		if IsInternalStoragePath(childRel) {
			continue
		}
		info, iErr := de.Info()
		if iErr != nil {
			continue
		}
		out = append(out, e.attrToInventoryEntry(layeredfs.QualifiedPath{RootID: nq.RootID, Rel: childRel}, layeredfs.EntryAttr{
			Layer: layeredfs.LayerTrunk, Kind: entryKindFromMode(info.Mode()), Mode: info.Mode(), Size: info.Size(), ModTime: info.ModTime(),
		}))
	}
	sortInventoryEntries(out)
	return out, nil
}

func (e *Engine) attrToInventoryEntry(nq layeredfs.QualifiedPath, attr layeredfs.EntryAttr) InventoryEntry {
	return InventoryEntry{
		Path:                 nq,
		RootLabel:            e.rootsByID[nq.RootID].Label,
		Kind:                 attr.Kind,
		Layer:                attr.Layer,
		Mode:                 attr.Mode,
		Size:                 attr.Size,
		ModTime:              attr.ModTime,
		LinkTarget:           attr.LinkTarget,
		IsIndexDerived:       false,
		ExactContentObserved: false,
		ObservedAt:           attr.ModTime,
		Coverage:             CoverageComplete,
		Source:               AccessSourceMountedView,
	}
}

func (e *Engine) listCheckpointDirectory(ctx context.Context, nq layeredfs.QualifiedPath, ckptID string) ([]InventoryEntry, error) {
	prefix := ""
	if nq.Rel != "." {
		prefix = nq.Rel
	}
	survey, err := e.SurveyBranch(ctx, SurveyOptions{
		RootIDs:      []string{nq.RootID},
		PathPrefix:   prefix,
		CheckpointID: ckptID,
	})
	if err != nil {
		return nil, err
	}
	children := make(map[string]InventoryEntry)
	for _, entry := range survey.Entries {
		if entry.Path.Rel == nq.Rel {
			continue
		}
		relToDir := entry.Path.Rel
		if nq.Rel != "." {
			if !strings.HasPrefix(entry.Path.Rel, nq.Rel+"/") {
				continue
			}
			relToDir = strings.TrimPrefix(entry.Path.Rel, nq.Rel+"/")
		}
		parts := strings.SplitN(relToDir, "/", 2)
		childRel := parts[0]
		if nq.Rel != "." {
			childRel = path.Join(nq.Rel, parts[0])
		}
		if len(parts) == 1 {
			children[childRel] = entry
		} else if _, exists := children[childRel]; !exists {
			children[childRel] = InventoryEntry{
				Path:           layeredfs.QualifiedPath{RootID: nq.RootID, Rel: childRel},
				RootLabel:      e.rootsByID[nq.RootID].Label,
				Kind:           layeredfs.EntryDirectory,
				Layer:          entry.Layer,
				IsIndexDerived: entry.IsIndexDerived,
				Coverage:       entry.Coverage,
				Source:         AccessSourceCheckpointStore,
			}
		}
	}
	out := make([]InventoryEntry, 0, len(children))
	for _, ch := range children {
		out = append(out, ch)
	}
	sortInventoryEntries(out)
	return out, nil
}

func (e *Engine) resolveRequestedRoots(requested []string) ([]string, map[string]bool, error) {
	if len(requested) == 0 {
		allowed := make(map[string]bool, len(e.rootOrder))
		for _, id := range e.rootOrder {
			allowed[id] = true
		}
		return append([]string(nil), e.rootOrder...), allowed, nil
	}
	var roots []string
	allowed := make(map[string]bool, len(requested))
	for _, raw := range requested {
		id := strings.TrimSpace(raw)
		if id == "" {
			continue
		}
		if _, ok := e.rootsByID[id]; !ok {
			return nil, nil, fmt.Errorf("%w: %s", ErrUnknownRoot, id)
		}
		if !allowed[id] {
			allowed[id] = true
			roots = append(roots, id)
		}
	}
	return roots, allowed, nil
}
