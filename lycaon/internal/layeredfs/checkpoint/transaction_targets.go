package checkpoint

import (
	"fmt"
	"io"
	"maps"
	"path/filepath"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/layeredfs"
)

func (txn *Transaction) captureTarget(path layeredfs.QualifiedPath) (targetImage, error) {
	obs, err := txn.targets.observeTarget(path)
	if err != nil {
		return targetImage{}, err
	}
	image := targetImage{Kind: obs.Kind, Mode: obs.Mode.Perm(), SHA256: obs.SHA256, Size: obs.Size, LinkTarget: obs.LinkTarget}
	if obs.Kind == layeredfs.EntryAbsent || obs.Kind == layeredfs.EntrySymlink {
		image.Mode = 0
		image.Size = 0
		return image, nil
	}
	root, err := txn.targets.openTargetRoot(path.RootID)
	if err != nil {
		return image, err
	}
	defer func() { _ = root.Close() }()
	file, err := root.Open(path.Rel)
	if err != nil {
		return image, err
	}
	defer func() { _ = file.Close() }()
	held, err := file.Stat()
	if err != nil {
		return image, err
	}
	if held.Mode().Perm() != obs.Mode.Perm() || held.Size() != obs.Size {
		return image, ErrStalePreviewTargetChanged
	}
	if obs.Kind == layeredfs.EntryRegular {
		image.identity, image.links, err = targetFileIdentityAndLinks(file, held)
		if err != nil {
			return image, err
		}
		image.HardLinkGroup = image.identity
	}
	image.Xattrs, err = readFileXattrs(file)
	if err != nil {
		return image, err
	}
	if obs.Kind == layeredfs.EntryRegular {
		body, err := io.ReadAll(file)
		if err != nil {
			return image, err
		}
		if sha256Hex(body) != obs.SHA256 {
			return image, ErrStalePreviewTargetChanged
		}
		if txn.keepOriginals {
			if err := writeAtomicJSONOrText(txn.dir, filepath.Join("originals", obs.SHA256), body, 0o600); err != nil {
				return image, err
			}
		}
	} else {
		image.Size = 0
	}
	return image, nil
}

func (txn *Transaction) captureTree(path layeredfs.QualifiedPath, images map[layeredfs.QualifiedPath]targetImage) error {
	if _, found := images[path]; found {
		return nil
	}
	image, err := txn.captureTarget(path)
	if err != nil {
		return err
	}
	images[path] = image
	if image.Kind != layeredfs.EntryDirectory {
		return nil
	}
	root, err := txn.targets.openTargetRoot(path.RootID)
	if err != nil {
		return err
	}
	entries, err := root.Open(path.Rel)
	if err != nil {
		_ = root.Close()
		return err
	}
	children, err := entries.ReadDir(-1)
	_ = entries.Close()
	_ = root.Close()
	if err != nil {
		return err
	}
	for _, child := range children {
		next := layeredfs.QualifiedPath{RootID: path.RootID, Rel: filepath.Join(path.Rel, child.Name())}
		if err := txn.captureTree(next, images); err != nil {
			return err
		}
	}
	return nil
}

// addTargets records undo for every path these changes touch, durably before any of them happens.
// A path an earlier stage recorded keeps its first before-image, which is what rollback restores.
func (txn *Transaction) addTargets(changes []PlannedChange) error {
	txn.keepOriginals = true
	defer func() { txn.keepOriginals = false }()
	before := make(map[layeredfs.QualifiedPath]targetImage, len(txn.journal.Targets))
	after := make(map[layeredfs.QualifiedPath]targetImage, len(txn.journal.Targets))
	for _, target := range txn.journal.Targets {
		before[target.Path], after[target.Path] = target.Before, target.After
	}
	for _, change := range changes {
		if err := txn.captureTree(change.Path, before); err != nil {
			return err
		}
		// Record parents created implicitly by MkdirAll, without capturing unrelated trees.
		for parent := filepath.Dir(change.Path.Rel); parent != "."; parent = filepath.Dir(parent) {
			path := layeredfs.QualifiedPath{RootID: change.Path.RootID, Rel: parent}
			if _, known := before[path]; known {
				continue
			}
			obs, err := txn.targets.observeTarget(path)
			if err != nil {
				return err
			}
			if obs.Kind != layeredfs.EntryAbsent {
				break
			}
			before[path] = targetImage{Kind: layeredfs.EntryAbsent}
		}
	}
	for path, img := range before {
		if _, known := after[path]; !known {
			img.HardLinkGroup, img.HardLinkSource, img.RetainedSlot = "", nil, ""
			after[path] = img
		}
	}
	for _, change := range changes {
		if err := txn.planTarget(after, change); err != nil {
			return err
		}
	}
	for i := range txn.journal.Targets {
		target := &txn.journal.Targets[i]
		if next := after[target.Path]; !sameTarget(next, target.After) {
			target.Prior = append(target.Prior, target.After)
			target.After = next
		}
	}
	fresh := slices.SortedFunc(maps.Keys(after), func(a, b layeredfs.QualifiedPath) int { return strings.Compare(pathKey(a), pathKey(b)) })
	fresh = slices.DeleteFunc(fresh, func(path layeredfs.QualifiedPath) bool { _, known := txn.index[path]; return known })
	if err := txn.declareSlots(fresh, before); err != nil {
		return err
	}
	for _, path := range fresh {
		image := before[path]
		idx := len(txn.journal.Targets)
		if image.Kind == layeredfs.EntryRegular {
			if err := txn.retainFirstLink(path, &image, idx, after); err != nil {
				return err
			}
			if err := txn.reach(StepOriginalRetained, pathKey(path)); err != nil {
				return err
			}
		}
		txn.index[path] = idx
		txn.journal.Targets = append(txn.journal.Targets, targetUndo{Path: path, Before: image, After: after[path]})
	}
	txn.journal.PendingSlots = nil
	return txn.saveJournal(txn.journal)
}

// declareSlots names the links about to exist, so a crash before the journal adopts them leaves
// entries recovery knows are this transaction's and not foreign.
func (txn *Transaction) declareSlots(fresh []layeredfs.QualifiedPath, before map[layeredfs.QualifiedPath]targetImage) error {
	base := len(txn.journal.Targets)
	txn.journal.PendingSlots = nil
	for i, path := range fresh {
		if before[path].Kind == layeredfs.EntryRegular {
			txn.journal.PendingSlots = append(txn.journal.PendingSlots, targetSlot(base+i))
		}
	}
	if len(txn.journal.PendingSlots) == 0 {
		return nil
	}
	return txn.saveJournal(txn.journal)
}

func targetSlot(idx int) string { return fmt.Sprintf("slot-%06d", idx) }

// retainFirstLink keeps the original inode for the first path of each link group; its aliases
// restore from that path.
func (txn *Transaction) retainFirstLink(path layeredfs.QualifiedPath, image *targetImage, idx int, after map[layeredfs.QualifiedPath]targetImage) error {
	key := path.RootID + "\x00" + image.identity
	if source, found := txn.origins[key]; found {
		image.HardLinkSource = &source
		image.RetainedSlot = txn.slots[key]
		return nil
	}
	txn.origins[key] = path
	slot, err := txn.retainTargetInode(path, *image, idx, after)
	if err != nil {
		return err
	}
	image.RetainedSlot = slot
	txn.slots[key] = slot
	if slot == "" && image.links <= 1 {
		// Restored from its original bytes, this path can no longer match the old inode.
		image.HardLinkGroup = ""
	}
	return nil
}

func (txn *Transaction) planTarget(images map[layeredfs.QualifiedPath]targetImage, ch PlannedChange) error {
	if ch.Action == ActionNoop {
		return nil
	}
	if ch.Action == ActionDelete || ch.Action == ActionReplaceWithDirectory || ch.Action == ActionReplaceDirWithRegular || ch.Action == ActionSymlink || ch.Action == ActionHardLink {
		for path := range images {
			if path.RootID == ch.Path.RootID && (path.Rel == ch.Path.Rel || strings.HasPrefix(path.Rel, ch.Path.Rel+string(filepath.Separator))) {
				images[path] = targetImage{Kind: layeredfs.EntryAbsent}
			}
		}
	}
	next := targetImage{}
	switch ch.Action {
	case ActionDelete:
		next.Kind = layeredfs.EntryAbsent
	case ActionMkdir, ActionReplaceWithDirectory:
		next = images[ch.Path]
		next.Kind = layeredfs.EntryDirectory
		next.Size = 0
		next.Mode = pickMode(ch.Mode, 0755).Perm()
		next.Xattrs = maps.Clone(next.Xattrs)
		if next.Xattrs == nil {
			next.Xattrs = map[string][]byte{}
		}
		maps.Copy(next.Xattrs, ch.Xattrs)
	case ActionWriteRegular, ActionReplaceDirWithRegular:
		body := ch.MergedBytes
		if body == nil && ch.MergedSHA256 != "" {
			var err error
			body, err = txn.targets.store.ReadBlob(ch.MergedSHA256)
			if err != nil {
				return err
			}
		}
		next = targetImage{Kind: layeredfs.EntryRegular, Mode: pickMode(ch.Mode, 0644).Perm(), SHA256: sha256Hex(body), Size: int64(len(body)), Xattrs: cloneXattrs(ch.Xattrs)}
	case ActionSymlink:
		next = targetImage{Kind: layeredfs.EntrySymlink, LinkTarget: ch.LinkTarget}
	case ActionHardLink:
		if ch.HardLinkSource == nil || ch.HardLinkSource.RootID != ch.Path.RootID {
			return fmt.Errorf("checkpoint: invalid hard-link source")
		}
		var found bool
		next, found = images[*ch.HardLinkSource]
		if !found {
			var err error
			next, err = txn.captureTarget(*ch.HardLinkSource)
			if err != nil {
				return err
			}
		}
		if next.Kind != layeredfs.EntryRegular {
			return fmt.Errorf("checkpoint: hard-link source is not regular")
		}
	default:
		return fmt.Errorf("checkpoint: unsupported promotion action %q", ch.Action)
	}
	images[ch.Path] = next
	if ch.Action != ActionDelete {
		for parent := filepath.Dir(ch.Path.Rel); parent != "."; parent = filepath.Dir(parent) {
			path := layeredfs.QualifiedPath{RootID: ch.Path.RootID, Rel: parent}
			if old, found := images[path]; found && old.Kind == layeredfs.EntryAbsent {
				images[path] = targetImage{Kind: layeredfs.EntryDirectory, Mode: 0755}
			}
		}
	}
	return nil
}

func (txn *Transaction) restoreTargets(targets []targetUndo) error {
	ordered := slices.Clone(targets)
	slices.SortFunc(ordered, func(a, b targetUndo) int { return len(b.Path.Rel) - len(a.Path.Rel) })
	// Remove only recorded entries, never an unobserved child added concurrently.
	for _, target := range ordered {
		current, err := txn.captureTarget(target.Path)
		if err != nil {
			return err
		}
		if sameTarget(current, target.Before) && txn.targetMatchesBeforeInode(target, current) {
			continue
		}
		if target.Before.HardLinkSource != nil && current.Kind == layeredfs.EntryAbsent {
			continue
		}
		if !target.holds(current) {
			return fmt.Errorf("checkpoint: target changed during recovery: %s", pathKey(target.Path))
		}
		if current.Kind != layeredfs.EntryAbsent && (target.Before.Kind == layeredfs.EntryAbsent || target.Before.Kind == "" || current.Kind != target.Before.Kind) {
			if err := txn.removeTargetEntry(target.Path); err != nil {
				return err
			}
		}
	}
	slices.Reverse(ordered)
	for _, target := range ordered {
		before := target.Before
		if before.Kind == layeredfs.EntryAbsent || before.Kind == "" || before.HardLinkSource != nil {
			continue
		}
		if before.Kind == layeredfs.EntryRegular {
			if err := txn.restoreRegularTarget(target); err != nil {
				return err
			}
			continue
		}
		change := PlannedChange{Path: target.Path, Mode: before.Mode, Xattrs: before.Xattrs}
		switch before.Kind {
		case layeredfs.EntryDirectory:
			change.Action = ActionMkdir
			change.Mode = before.Mode.Perm() | 0o700
		case layeredfs.EntrySymlink:
			change.Action = ActionSymlink
			change.LinkTarget = before.LinkTarget
		default:
			return ErrInvalidManifest
		}
		current, err := txn.captureTarget(target.Path)
		if err != nil {
			return err
		}
		if before.Kind == layeredfs.EntrySymlink && sameTarget(current, before) {
			continue
		}
		if err := txn.targets.applyPlannedChange(change); err != nil {
			return err
		}
	}
	if err := txn.restoreHardLinks(targets); err != nil {
		return err
	}
	return txn.finalizeRestoredDirectories(ordered)
}
