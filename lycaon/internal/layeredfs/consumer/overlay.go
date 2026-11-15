package consumer

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/checkpoint"
)

type changeDelta struct {
	entries         map[layeredfs.QualifiedPath]InventoryEntry
	deleted         map[layeredfs.QualifiedPath]struct{}
	replacedSubtree []layeredfs.QualifiedPath
	divergence      []DivergenceEntry
	source          AccessSource
}

// ComputeDivergence computes O(changed nodes) workspace divergence and promotion preview entries
// from the live mutation index (when checkpointID == "") or an immutable checkpoint manifest,
// without walking untouched lower directories or hashing the full branch tree.
func (e *Engine) ComputeDivergence(_ context.Context, checkpointID string) (DivergenceSummary, error) {
	delta, err := e.collectChangeDelta(strings.TrimSpace(checkpointID), nil, "")
	if err != nil {
		return DivergenceSummary{}, err
	}
	paths := make([]layeredfs.QualifiedPath, 0, len(delta.divergence))
	for _, d := range delta.divergence {
		paths = append(paths, d.Path)
	}
	return DivergenceSummary{
		ViewID:        e.viewID,
		CheckpointID:  strings.TrimSpace(checkpointID),
		HasDivergence: len(delta.divergence) > 0,
		Entries:       delta.divergence,
		Paths:         paths,
		Source:        delta.source,
	}, nil
}

// HasWorkspaceDivergence reports whether the worker view has any logical divergence from its base
// in O(changed nodes) time without walking lower directories.
func (e *Engine) HasWorkspaceDivergence(ctx context.Context) (bool, error) {
	sum, err := e.ComputeDivergence(ctx, "")
	if err != nil {
		return false, err
	}
	return sum.HasDivergence, nil
}

func (e *Engine) collectChangeDelta(
	checkpointID string,
	allowedRoots map[string]bool,
	pathPrefix string,
) (changeDelta, error) {
	if checkpointID != "" {
		if e.ckptStore == nil {
			return changeDelta{}, ErrCheckpointNotConfigured
		}
		m, err := e.ckptStore.LoadManifest(checkpointID)
		if err != nil {
			return changeDelta{}, err
		}
		return e.deltaFromCheckpointManifest(m, allowedRoots, pathPrefix), nil
	}
	if e.core == nil {
		return changeDelta{
			entries: make(map[layeredfs.QualifiedPath]InventoryEntry),
			deleted: make(map[layeredfs.QualifiedPath]struct{}),
			source:  AccessSourceIndexOverlay,
		}, nil
	}
	changes, err := e.core.EnumerateChanges(false)
	if err != nil {
		return changeDelta{}, err
	}
	ops := e.core.JournalOperations()
	return e.deltaFromLiveChanges(changes, ops, allowedRoots, pathPrefix), nil
}

func (e *Engine) deltaFromLiveChanges(
	changes []layeredfs.ChangeEntry,
	ops []layeredfs.JournalOperation,
	allowedRoots map[string]bool,
	pathPrefix string,
) changeDelta {
	out := changeDelta{
		entries: make(map[layeredfs.QualifiedPath]InventoryEntry),
		deleted: make(map[layeredfs.QualifiedPath]struct{}),
		source:  AccessSourceIndexOverlay,
	}
	renameOrigins := make(map[layeredfs.QualifiedPath]layeredfs.QualifiedPath)
	for _, op := range ops {
		if (op.Kind == layeredfs.JournalOpRename || op.Kind == layeredfs.JournalOpRenameOver) && op.SourcePath != nil {
			renameOrigins[op.Path] = *op.SourcePath
			if allowedRoots == nil || allowedRoots[op.SourcePath.RootID] {
				out.deleted[*op.SourcePath] = struct{}{}
				if op.EntryKind == layeredfs.EntryDirectory {
					out.replacedSubtree = append(out.replacedSubtree, *op.SourcePath)
				}
			}
		}
	}
	for _, ch := range changes {
		if !ch.LogicalChange && !ch.Deleted && !ch.DirectoryReplaced {
			continue
		}
		e.applyChangeToDelta(&out, ch, ch.Final.ModTime, AccessSourceIndexOverlay, renameOrigins, allowedRoots, pathPrefix)
	}
	sortDivergence(out.divergence)
	return out
}

func (e *Engine) deltaFromCheckpointManifest(
	m checkpoint.Manifest,
	allowedRoots map[string]bool,
	pathPrefix string,
) changeDelta {
	out := changeDelta{
		entries: make(map[layeredfs.QualifiedPath]InventoryEntry),
		deleted: make(map[layeredfs.QualifiedPath]struct{}),
		source:  AccessSourceCheckpointStore,
	}
	for _, entry := range m.Entries {
		ch := layeredfs.ChangeEntry{
			Path:              entry.Path,
			Kind:              entry.Kind,
			Deleted:           entry.Deleted,
			LogicalChange:     true,
			ContentChanged:    entry.ContentChanged,
			MetadataChanged:   entry.MetadataChanged,
			DirectoryReplaced: entry.DirectoryReplaced,
			Base:              entry.Base,
			Final:             entry.Final,
		}
		e.applyChangeToDelta(&out, ch, m.CreatedAt, AccessSourceCheckpointStore, nil, allowedRoots, pathPrefix)
	}
	sortDivergence(out.divergence)
	return out
}

func (e *Engine) applyChangeToDelta(
	out *changeDelta,
	ch layeredfs.ChangeEntry,
	observedAt time.Time,
	source AccessSource,
	renameOrigins map[layeredfs.QualifiedPath]layeredfs.QualifiedPath,
	allowedRoots map[string]bool,
	pathPrefix string,
) {
	if allowedRoots != nil && !allowedRoots[ch.Path.RootID] {
		return
	}
	if IsInternalStoragePath(ch.Path.Rel) {
		return
	}
	if ch.DirectoryReplaced || (ch.Base.Kind == layeredfs.EntryDirectory && ch.Kind != layeredfs.EntryDirectory) ||
		(ch.Base.Kind != layeredfs.EntryAbsent && ch.Base.Kind != layeredfs.EntryDirectory && ch.Kind == layeredfs.EntryDirectory) {
		out.replacedSubtree = append(out.replacedSubtree, ch.Path)
	}
	if ch.Deleted || ch.Kind == layeredfs.EntryAbsent {
		out.deleted[ch.Path] = struct{}{}
		if ch.Base.Kind == layeredfs.EntryDirectory {
			out.replacedSubtree = append(out.replacedSubtree, ch.Path)
		}
	} else if matchesPrefix(ch.Path.Rel, pathPrefix) {
		out.entries[ch.Path] = InventoryEntry{
			Path:                 ch.Path,
			RootLabel:            e.rootsByID[ch.Path.RootID].Label,
			Kind:                 ch.Kind,
			Layer:                layeredfs.LayerPrivate,
			Mode:                 ch.Final.Mode,
			Size:                 ch.Final.Size,
			ModTime:              ch.Final.ModTime,
			SHA256:               ch.Final.SHA256,
			LinkTarget:           ch.Final.LinkTarget,
			IsIndexDerived:       false,
			ExactContentObserved: ch.Final.SHA256 != "",
			ObservedAt:           observedAt,
			Coverage:             CoverageComplete,
			Source:               source,
		}
	}
	if matchesPrefix(ch.Path.Rel, pathPrefix) {
		out.divergence = append(out.divergence, classifyDivergence(ch, renameOrigins))
	}
}

func (e *Engine) mergeIndexAndDelta(
	idxRes SourceIndexResult,
	delta changeDelta,
	allowedRoots map[string]bool,
	pathPrefix string,
	maxEntries int,
) ([]InventoryEntry, bool) {
	merged := make(map[layeredfs.QualifiedPath]InventoryEntry, len(idxRes.Entries)+len(delta.entries))
	for _, ie := range idxRes.Entries {
		nq, err := normalizeQualifiedPath(ie.Path)
		if err != nil || nq.Rel == "." || IsInternalStoragePath(nq.Rel) {
			continue
		}
		if (allowedRoots != nil && !allowedRoots[nq.RootID]) || !matchesPrefix(nq.Rel, pathPrefix) || isSuppressedByDelta(nq, delta) {
			continue
		}
		obsAt := ie.ObservedAt
		if obsAt.IsZero() {
			obsAt = idxRes.ObservedAt
		}
		cov := ie.Freshness.Coverage
		if cov == "" {
			cov = idxRes.Coverage
		}
		if cov == "" {
			cov = CoverageComplete
		}
		fresh := ie.Freshness
		fresh.ObservedAt = obsAt
		fresh.Coverage = cov
		merged[nq] = InventoryEntry{
			Path:                 nq,
			RootLabel:            e.rootsByID[nq.RootID].Label,
			Kind:                 ie.Kind,
			Layer:                layeredfs.LayerTrunk,
			Mode:                 ie.Mode,
			Size:                 ie.Size,
			ModTime:              ie.ModTime,
			SHA256:               "", // Index-derived entries are never labeled as exact content observations.
			IsIndexDerived:       true,
			ExactContentObserved: false,
			ObservedAt:           obsAt,
			Coverage:             cov,
			Freshness:            fresh,
			Source:               AccessSourceIndexOverlay,
		}
	}
	for qp, pe := range delta.entries {
		merged[qp] = pe
	}
	out := make([]InventoryEntry, 0, len(merged))
	for _, entry := range merged {
		out = append(out, entry)
	}
	sortInventoryEntries(out)
	truncated := idxRes.Truncated
	if maxEntries > 0 && len(out) > maxEntries {
		out = out[:maxEntries]
		truncated = true
	}
	return out, truncated
}

func isSuppressedByDelta(qpath layeredfs.QualifiedPath, delta changeDelta) bool {
	if _, del := delta.deleted[qpath]; del {
		return true
	}
	for _, rep := range delta.replacedSubtree {
		if rep.RootID == qpath.RootID && isDescendantRel(qpath.Rel, rep.Rel) {
			return true
		}
	}
	return false
}

func classifyDivergence(ch layeredfs.ChangeEntry, renameOrigins map[layeredfs.QualifiedPath]layeredfs.QualifiedPath) DivergenceEntry {
	kind := DivergenceModified
	var prev *layeredfs.QualifiedPath
	if orig, ok := renameOrigins[ch.Path]; ok {
		cp := orig
		prev = &cp
		kind = DivergenceRenamed
	} else if ch.Deleted || ch.Kind == layeredfs.EntryAbsent {
		kind = DivergenceDeleted
	} else if ch.DirectoryReplaced || (ch.Base.Kind == layeredfs.EntryDirectory && ch.Kind != layeredfs.EntryDirectory) {
		kind = DivergenceReplacedDir
	} else if ch.Base.Kind == layeredfs.EntryAbsent {
		kind = DivergenceAdded
	}
	return DivergenceEntry{
		Path:              ch.Path,
		PreviousPath:      prev,
		Kind:              kind,
		EntryKind:         ch.Kind,
		DirectoryReplaced: ch.DirectoryReplaced,
		ContentChanged:    ch.ContentChanged,
		MetadataChanged:   ch.MetadataChanged,
		FinalSHA256:       ch.Final.SHA256,
		BaseSHA256:        ch.Base.SHA256,
		Size:              ch.Final.Size,
	}
}

func matchesPrefix(rel, prefix string) bool {
	prefix = strings.Trim(strings.TrimSpace(prefix), "/")
	if prefix == "" || prefix == "." {
		return true
	}
	return rel == prefix || strings.HasPrefix(rel, prefix+"/")
}

func sortDivergence(entries []DivergenceEntry) {
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Path.RootID != entries[j].Path.RootID {
			return entries[i].Path.RootID < entries[j].Path.RootID
		}
		return entries[i].Path.Rel < entries[j].Path.Rel
	})
}

func effectiveObservedAt(t time.Time) time.Time {
	if t.IsZero() {
		return time.Now().UTC()
	}
	return t
}
