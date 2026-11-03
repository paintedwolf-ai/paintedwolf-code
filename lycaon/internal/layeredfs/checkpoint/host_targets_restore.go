package checkpoint

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"

	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/layeredfs"
)

func (host *HostPromotion) restoreHostTargets(targets []hostTargetUndo) error {
	ordered := slices.Clone(targets)
	slices.SortFunc(ordered, func(a, b hostTargetUndo) int { return len(b.Path.Rel) - len(a.Path.Rel) })
	// Remove only recorded entries, never an unobserved child added concurrently.
	for _, target := range ordered {
		current, err := host.captureHostTarget(target.Path)
		if err != nil {
			return err
		}
		if sameHostTarget(current, target.Before) {
			continue
		}
		if !sameHostTarget(current, target.After) {
			return fmt.Errorf("checkpoint: target changed during recovery: %s", pathKey(target.Path))
		}
		if current.Kind != layeredfs.EntryAbsent && (target.Before.Kind == layeredfs.EntryAbsent || target.Before.Kind == "" || current.Kind != target.Before.Kind) {
			root, err := host.targets.openTargetRoot(target.Path.RootID)
			if err != nil {
				return err
			}
			err = root.Remove(target.Path.Rel)
			_ = root.Close()
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
	slices.Reverse(ordered)
	linkPrimary := map[string]layeredfs.QualifiedPath{}
	for _, target := range ordered {
		before := target.Before
		if before.Kind == layeredfs.EntryAbsent || before.Kind == "" {
			continue
		}
		change, err := planRestoreTargetChange(host, target.Path, before, linkPrimary)
		if err != nil {
			return err
		}
		current, err := host.captureHostTarget(target.Path)
		if err != nil {
			return err
		}
		if sameHostTarget(current, before) && (change.Action != ActionHardLink || sameTargetFile(host, target.Path, *change.HardLinkSource)) {
			continue
		}
		if err := host.targets.applyPlannedChange(change); err != nil {
			return err
		}
		if before.Kind == layeredfs.EntryRegular || before.Kind == layeredfs.EntryDirectory {
			if err := restoreTargetMetadata(host, target.Path, before); err != nil {
				return err
			}
		}
	}
	return nil
}

func planRestoreTargetChange(
	host *HostPromotion,
	path layeredfs.QualifiedPath,
	before hostTargetImage,
	linkPrimary map[string]layeredfs.QualifiedPath,
) (PlannedChange, error) {
	change := PlannedChange{Path: path, Mode: before.Mode, Xattrs: before.Xattrs}
	switch before.Kind {
	case layeredfs.EntryRegular:
		change.Action = ActionWriteRegular
		change.MergedSHA256 = before.SHA256
		if before.HardLinkGroup != "" {
			groupKey := path.RootID + "\x00" + before.HardLinkGroup
			if src, ok := linkPrimary[groupKey]; ok {
				srcCopy := src
				change.Action = ActionHardLink
				change.HardLinkSource = &srcCopy
			} else if peer, ok, err := findHardLinkPeer(host, path, before); err != nil {
				return PlannedChange{}, err
			} else if ok {
				linkPrimary[groupKey] = peer
				peerCopy := peer
				change.Action = ActionHardLink
				change.HardLinkSource = &peerCopy
			} else {
				linkPrimary[groupKey] = path
			}
		}
	case layeredfs.EntryDirectory:
		change.Action = ActionMkdir
	case layeredfs.EntrySymlink:
		change.Action = ActionSymlink
		change.LinkTarget = before.LinkTarget
	default:
		return PlannedChange{}, ErrInvalidManifest
	}
	return change, nil
}

func restoreTargetMetadata(host *HostPromotion, path layeredfs.QualifiedPath, before hostTargetImage) error {
	root, err := host.targets.openTargetRoot(path.RootID)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	file, err := root.Open(path.Rel)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	if before.Mode != 0 {
		if err := file.Chmod(before.Mode.Perm()); err != nil {
			return err
		}
	}
	if err := restoreFileXattrs(file, before.Xattrs); err != nil {
		return err
	}
	return syncFile(file)
}

func findHardLinkPeer(host *HostPromotion, path layeredfs.QualifiedPath, before hostTargetImage) (layeredfs.QualifiedPath, bool, error) {
	root, err := host.targets.openTargetRoot(path.RootID)
	if err != nil {
		return layeredfs.QualifiedPath{}, false, err
	}
	defer func() { _ = root.Close() }()
	var foundRel string
	errStop := errors.New("stop")
	walkErr := fs.WalkDir(root.FS(), ".", func(rel string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if rel == ".git" || filepath.Base(rel) == ".git" {
				return fs.SkipDir
			}
			return nil
		}
		if rel == path.Rel || !d.Type().IsRegular() {
			return nil
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return nil
		}
		fobs := fspath.ObserveFile(info)
		if fobs.Known && fmt.Sprintf("%d:%d", fobs.Identity.Device, fobs.Identity.Inode) == before.HardLinkGroup {
			foundRel = rel
			return errStop
		}
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, errStop) {
		return layeredfs.QualifiedPath{}, false, walkErr
	}
	if foundRel == "" {
		return layeredfs.QualifiedPath{}, false, nil
	}
	peer := layeredfs.QualifiedPath{RootID: path.RootID, Rel: foundRel}
	peerImage, err := host.captureHostTarget(peer)
	if err != nil {
		return layeredfs.QualifiedPath{}, false, err
	}
	if !sameHostTarget(peerImage, before) {
		return layeredfs.QualifiedPath{}, false, fmt.Errorf("checkpoint: cannot recover diverged hard-link peer %s", pathKey(peer))
	}
	return peer, true, nil
}

func sameTargetFile(host *HostPromotion, a, b layeredfs.QualifiedPath) bool {
	if a.RootID != b.RootID {
		return false
	}
	root, err := host.targets.openTargetRoot(a.RootID)
	if err != nil {
		return false
	}
	defer func() { _ = root.Close() }()
	infoA, errA := root.Lstat(a.Rel)
	infoB, errB := root.Lstat(b.Rel)
	return errA == nil && errB == nil && os.SameFile(infoA, infoB)
}
