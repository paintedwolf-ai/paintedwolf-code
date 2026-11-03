package checkpoint

import (
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/layeredfs"
)

func (pr *Promoter) evaluateEntry(
	entry ManifestEntry,
	cur TargetObservation,
	groupPrimary map[string]layeredfs.QualifiedPath,
) (PlannedChange, *PromotionConflict, bool, error) {
	if len(entry.CandidateBases) > 0 {
		conf, err := pr.evaluateAmbiguousCandidates(entry, cur)
		return PlannedChange{}, conf, false, err
	}
	if entry.Base.Kind == "" {
		return PlannedChange{}, newConflict(entry, cur, ConflictMissingExactBase, "missing exact base provenance"), false, nil
	}
	if entry.Deleted || entry.Kind == layeredfs.EntryAbsent {
		return pr.evaluateDeletion(entry, cur)
	}
	if entry.DirectoryReplaced {
		return pr.evaluateDirReplacement(entry, cur)
	}
	switch entry.Kind {
	case layeredfs.EntryDirectory:
		return pr.evaluateDirectory(entry, cur)
	case layeredfs.EntrySymlink:
		return pr.evaluateSymlink(entry, cur)
	case layeredfs.EntryRegular:
		return pr.evaluateRegular(entry, cur, groupPrimary)
	default:
		return PlannedChange{}, newConflict(entry, cur, ConflictTypeMismatch, "unsupported entry kind"), false, nil
	}
}

func (pr *Promoter) evaluateAmbiguousCandidates(
	entry ManifestEntry,
	cur TargetObservation,
) (*PromotionConflict, error) {
	var workerBytes, targetBytes []byte
	if !entry.Deleted && entry.Kind == layeredfs.EntryRegular && entry.Final.SHA256 != "" {
		b, err := pr.cfg.Store.ReadBlob(entry.Final.SHA256)
		if err != nil {
			return nil, err
		}
		workerBytes = b
	}
	if cur.Kind == layeredfs.EntryRegular {
		rootDir := pr.cfg.TargetRoots[entry.Path.RootID]
		b, err := os.ReadFile(filepath.Join(rootDir, filepath.FromSlash(entry.Path.Rel)))
		if err != nil {
			return nil, err
		}
		targetBytes = b
	}
	var outputs [][]byte
	for _, cand := range entry.CandidateBases {
		if cand.Kind == layeredfs.EntryRegular && cand.SHA256 != "" {
			baseBytes, err := pr.cfg.Store.ReadBlob(cand.SHA256)
			if err != nil {
				continue
			}
			if merged, ok := ThreeWayMergeLines(baseBytes, targetBytes, workerBytes); ok {
				if !containsBytesSlice(outputs, merged) {
					outputs = append(outputs, merged)
				}
			}
		}
	}
	conf := newConflict(entry, cur, ConflictAmbiguousBase, "exact base provenance unavailable; candidate bases produce ambiguous merge")
	conf.CandidateBases = append([]layeredfs.BaseCapture(nil), entry.CandidateBases...)
	conf.AmbiguousOutputs = outputs
	return conf, nil
}

func (pr *Promoter) evaluateDeletion(
	entry ManifestEntry,
	cur TargetObservation,
) (PlannedChange, *PromotionConflict, bool, error) {
	if cur.Kind == layeredfs.EntryAbsent {
		return PlannedChange{Path: entry.Path, Action: ActionNoop}, nil, false, nil
	}
	if !targetMatchesBase(cur, entry.Base) {
		return PlannedChange{}, newConflict(entry, cur, ConflictModifyDelete, "target modified while worker deleted path"), false, nil
	}
	return PlannedChange{
		Path:              entry.Path,
		Action:            ActionDelete,
		DirectoryReplaced: entry.DirectoryReplaced,
	}, nil, false, nil
}

func (pr *Promoter) evaluateDirReplacement(
	entry ManifestEntry,
	cur TargetObservation,
) (PlannedChange, *PromotionConflict, bool, error) {
	if !targetMatchesBase(cur, entry.Base) {
		return PlannedChange{}, newConflict(entry, cur, ConflictDirectoryReplaced, "target changed across directory replacement"), false, nil
	}
	if entry.Kind == layeredfs.EntryDirectory {
		return PlannedChange{
			Path:              entry.Path,
			Action:            ActionReplaceWithDirectory,
			Mode:              entry.Final.Mode,
			Xattrs:            cloneXattrs(entry.Final.Xattrs),
			DirectoryReplaced: true,
		}, nil, false, nil
	}
	workerBytes, err := pr.cfg.Store.ReadBlob(entry.Final.SHA256)
	if err != nil {
		return PlannedChange{}, nil, false, err
	}
	return PlannedChange{
		Path:              entry.Path,
		Action:            ActionReplaceDirWithRegular,
		Mode:              entry.Final.Mode,
		MergedBytes:       workerBytes,
		MergedSHA256:      entry.Final.SHA256,
		Xattrs:            cloneXattrs(entry.Final.Xattrs),
		DirectoryReplaced: true,
	}, nil, false, nil
}

func (pr *Promoter) evaluateDirectory(
	entry ManifestEntry,
	cur TargetObservation,
) (PlannedChange, *PromotionConflict, bool, error) {
	if cur.Kind == layeredfs.EntryAbsent {
		return PlannedChange{
			Path:   entry.Path,
			Action: ActionMkdir,
			Mode:   entry.Final.Mode,
			Xattrs: cloneXattrs(entry.Final.Xattrs),
		}, nil, false, nil
	}
	if cur.Kind != layeredfs.EntryDirectory {
		return PlannedChange{}, newConflict(entry, cur, ConflictDirectoryReplaced, "target is not a directory"), false, nil
	}
	return PlannedChange{Path: entry.Path, Action: ActionNoop}, nil, false, nil
}

func (pr *Promoter) evaluateSymlink(
	entry ManifestEntry,
	cur TargetObservation,
) (PlannedChange, *PromotionConflict, bool, error) {
	if cur.Kind != layeredfs.EntryAbsent && !targetMatchesBase(cur, entry.Base) {
		if cur.Kind == layeredfs.EntrySymlink && cur.LinkTarget == entry.Final.LinkTarget {
			return PlannedChange{Path: entry.Path, Action: ActionNoop}, nil, false, nil
		}
		return PlannedChange{}, newConflict(entry, cur, ConflictTypeMismatch, "symlink target diverged"), false, nil
	}
	return PlannedChange{
		Path:       entry.Path,
		Action:     ActionSymlink,
		Mode:       entry.Final.Mode,
		LinkTarget: entry.Final.LinkTarget,
	}, nil, false, nil
}

func (pr *Promoter) evaluateRegular(
	entry ManifestEntry,
	cur TargetObservation,
	groupPrimary map[string]layeredfs.QualifiedPath,
) (PlannedChange, *PromotionConflict, bool, error) {
	workerBytes, err := pr.cfg.Store.ReadBlob(entry.Final.SHA256)
	if err != nil {
		return PlannedChange{}, nil, false, err
	}

	if entry.Final.HardLinkGroup != "" {
		if primary, exists := groupPrimary[entry.Final.HardLinkGroup]; exists {
			if !targetMatchesBase(cur, entry.Base) {
				return PlannedChange{}, newConflict(entry, cur, ConflictContentOverlap, "hard-link destination modified in target"), false, nil
			}
			srcCopy := primary
			return PlannedChange{
				Path:           entry.Path,
				Action:         ActionHardLink,
				Mode:           entry.Final.Mode,
				HardLinkGroup:  entry.Final.HardLinkGroup,
				HardLinkSource: &srcCopy,
			}, nil, false, nil
		}
	}

	if entry.Base.Kind == layeredfs.EntryAbsent {
		if cur.Kind != layeredfs.EntryAbsent {
			if cur.Kind == layeredfs.EntryRegular && cur.SHA256 == entry.Final.SHA256 {
				return PlannedChange{Path: entry.Path, Action: ActionNoop}, nil, false, nil
			}
			return PlannedChange{}, newConflict(entry, cur, ConflictAddAddDifference, "target also added path with different content"), false, nil
		}
		if entry.Final.HardLinkGroup != "" {
			groupPrimary[entry.Final.HardLinkGroup] = entry.Path
		}
		return PlannedChange{
			Path:          entry.Path,
			Action:        ActionWriteRegular,
			Mode:          entry.Final.Mode,
			MergedBytes:   workerBytes,
			MergedSHA256:  entry.Final.SHA256,
			HardLinkGroup: entry.Final.HardLinkGroup,
			Xattrs:        cloneXattrs(entry.Final.Xattrs),
		}, nil, false, nil
	}

	if entry.Base.Kind != layeredfs.EntryRegular || entry.Base.SHA256 == "" {
		return PlannedChange{}, newConflict(entry, cur, ConflictMissingExactBase, "missing exact regular base"), false, nil
	}
	baseBytes, bErr := pr.cfg.Store.ReadBlob(entry.Base.SHA256)
	if bErr != nil {
		return PlannedChange{}, newConflict(entry, cur, ConflictInvalidBaseDigest, bErr.Error()), false, nil
	}
	if int64(len(baseBytes)) != entry.Base.Size {
		return PlannedChange{}, newConflict(entry, cur, ConflictInvalidBaseDigest, "base size mismatch"), false, nil
	}

	if cur.Kind == layeredfs.EntryAbsent {
		return PlannedChange{}, newConflict(entry, cur, ConflictModifyDelete, "target deleted file modified by worker"), false, nil
	}
	if cur.Kind != layeredfs.EntryRegular {
		return PlannedChange{}, newConflict(entry, cur, ConflictTypeMismatch, "target replaced regular file with non-regular entry"), false, nil
	}

	if cur.SHA256 == entry.Base.SHA256 {
		if entry.Final.HardLinkGroup != "" {
			groupPrimary[entry.Final.HardLinkGroup] = entry.Path
		}
		return PlannedChange{
			Path:          entry.Path,
			Action:        ActionWriteRegular,
			Mode:          entry.Final.Mode,
			MergedBytes:   workerBytes,
			MergedSHA256:  entry.Final.SHA256,
			HardLinkGroup: entry.Final.HardLinkGroup,
			Xattrs:        cloneXattrs(entry.Final.Xattrs),
		}, nil, false, nil
	}

	rootDir := pr.cfg.TargetRoots[entry.Path.RootID]
	targetBytes, err := os.ReadFile(filepath.Join(rootDir, filepath.FromSlash(entry.Path.Rel)))
	if err != nil {
		return PlannedChange{}, nil, false, err
	}
	mergedBytes, ok := ThreeWayMergeLines(baseBytes, targetBytes, workerBytes)
	if !ok {
		return PlannedChange{}, newConflict(entry, cur, ConflictContentOverlap, "overlapping changes between source and worker"), false, nil
	}
	if entry.Final.HardLinkGroup != "" {
		groupPrimary[entry.Final.HardLinkGroup] = entry.Path
	}
	return PlannedChange{
		Path:          entry.Path,
		Action:        ActionWriteRegular,
		Mode:          entry.Final.Mode,
		MergedBytes:   mergedBytes,
		MergedSHA256:  sha256Hex(mergedBytes),
		HardLinkGroup: entry.Final.HardLinkGroup,
		Xattrs:        cloneXattrs(entry.Final.Xattrs),
	}, nil, true, nil
}
