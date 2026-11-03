package checkpoint

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/layeredfs"
)

func (host *HostPromotion) retainTargetInode(
	path layeredfs.QualifiedPath,
	image hostTargetImage,
	idx int,
	after map[layeredfs.QualifiedPath]hostTargetImage,
) (string, error) {
	rootDir := host.targets.roots[path.RootID]
	if rootDir == "" {
		return "", layeredfs.ErrInvalidRoot
	}
	slot := fmt.Sprintf("slot-%06d", idx)
	err := fseffect.HardLink(fseffect.HardLinkRequest{
		Source:   fseffect.Location{Root: rootDir, Rel: path.Rel},
		Location: fseffect.Location{Root: host.store.promotionsDir, Rel: filepath.Join("host-retained", slot)},
		DirMode:  0o700,
		Replace:  true,
	})
	if err != nil {
		if errors.Is(err, syscall.EXDEV) && host.hasSurvivingInRootPeer(path.RootID, image.identity, after) {
			return "", nil
		}
		if errors.Is(err, syscall.EXDEV) {
			return "", fmt.Errorf("checkpoint: cannot retain target inode across filesystems for %s: %w", pathKey(path), err)
		}
		return "", err
	}
	retained, rErr := host.captureRetainedTarget(slot)
	if rErr != nil {
		_ = os.Remove(filepath.Join(host.retainedDir(), slot))
		return "", rErr
	}
	if retained.identity != image.identity || !sameHostTarget(retained, image) {
		_ = os.Remove(filepath.Join(host.retainedDir(), slot))
		return "", ErrStalePreviewTargetChanged
	}
	return slot, nil
}

func (host *HostPromotion) hasSurvivingInRootPeer(rootID, identity string, after map[layeredfs.QualifiedPath]hostTargetImage) bool {
	if identity == "" {
		return false
	}
	root, err := host.targets.openTargetRoot(rootID)
	if err != nil {
		return false
	}
	defer func() { _ = root.Close() }()
	found := false
	_ = fs.WalkDir(root.FS(), ".", func(rel string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || rel == "." || entry.IsDir() || !entry.Type().IsRegular() {
			return nil
		}
		qp := layeredfs.QualifiedPath{RootID: rootID, Rel: filepath.Clean(rel)}
		if _, modified := after[qp]; modified {
			return nil
		}
		file, openErr := root.Open(rel)
		if openErr != nil {
			return nil
		}
		info, statErr := file.Stat()
		id, _, idErr := targetFileIdentityAndLinks(file, info)
		_ = file.Close()
		if statErr == nil && idErr == nil && id == identity {
			found = true
			return fs.SkipAll
		}
		return nil
	})
	return found
}

func (host *HostPromotion) validateRetainedSlot(path layeredfs.QualifiedPath, before hostTargetImage) error {
	if before.Kind != layeredfs.EntryRegular || before.RetainedSlot == "" {
		return nil
	}
	retained, err := host.captureRetainedTarget(before.RetainedSlot)
	if err != nil {
		return fmt.Errorf("checkpoint: missing retained inode slot %s for %s: %w", before.RetainedSlot, pathKey(path), err)
	}
	if !sameHostTarget(retained, before) || (before.HardLinkGroup != "" && retained.identity != before.HardLinkGroup) {
		return fmt.Errorf("checkpoint: cannot recover diverged retained inode for %s", pathKey(path))
	}
	return nil
}

func (host *HostPromotion) captureRetainedTarget(slot string) (hostTargetImage, error) {
	if !hostComponent(slot) {
		return hostTargetImage{}, ErrInvalidManifest
	}
	p := filepath.Join(host.retainedDir(), slot)
	file, err := os.Open(p)
	if err != nil {
		return hostTargetImage{}, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return hostTargetImage{}, err
	}
	if !info.Mode().IsRegular() {
		return hostTargetImage{}, ErrInvalidManifest
	}
	id, links, err := targetFileIdentityAndLinks(file, info)
	if err != nil {
		return hostTargetImage{}, err
	}
	attrs, err := readFileXattrs(file)
	if err != nil {
		return hostTargetImage{}, err
	}
	body, err := io.ReadAll(file)
	if err != nil {
		return hostTargetImage{}, err
	}
	return hostTargetImage{
		Kind:     layeredfs.EntryRegular,
		Mode:     info.Mode().Perm(),
		SHA256:   sha256Hex(body),
		Size:     info.Size(),
		Xattrs:   attrs,
		identity: id,
		links:    links,
	}, nil
}

func (host *HostPromotion) targetMatchesBeforeInode(target hostTargetUndo, current hostTargetImage) bool {
	before := target.Before
	if before.Kind != layeredfs.EntryRegular {
		return true
	}
	if before.RetainedSlot != "" {
		retained, err := host.captureRetainedTarget(before.RetainedSlot)
		if err != nil {
			return false
		}
		return current.identity != "" && current.identity == retained.identity && (before.HardLinkGroup == "" || retained.identity == before.HardLinkGroup)
	}
	if before.HardLinkSource != nil {
		if srcImg, err := host.captureHostTarget(*before.HardLinkSource); err == nil {
			return current.identity != "" && current.identity == srcImg.identity
		}
		return false
	}
	if before.HardLinkGroup != "" {
		return current.identity == before.HardLinkGroup
	}
	return true
}

func (host *HostPromotion) restoreRegularTarget(target hostTargetUndo) error {
	before := target.Before
	current, err := host.captureHostTarget(target.Path)
	if err != nil {
		return err
	}
	if sameHostTarget(current, before) && host.targetMatchesBeforeInode(target, current) {
		return nil
	}
	rootDir := host.targets.roots[target.Path.RootID]
	if rootDir == "" {
		return layeredfs.ErrInvalidRoot
	}
	root, err := host.targets.openTargetRoot(target.Path.RootID)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()

	return withWritableParent(root, target.Path.Rel, func() error {
		if err := host.targets.ensureTargetParents(root, target.Path); err != nil {
			return err
		}
		if before.RetainedSlot != "" {
			retained, rErr := host.captureRetainedTarget(before.RetainedSlot)
			if rErr != nil {
				return fmt.Errorf("checkpoint: missing retained inode slot %s for %s: %w", before.RetainedSlot, pathKey(target.Path), rErr)
			}
			if !sameHostTarget(retained, before) || (before.HardLinkGroup != "" && retained.identity != before.HardLinkGroup) {
				return fmt.Errorf("checkpoint: retained inode diverged for %s", pathKey(target.Path))
			}
			if err := fseffect.HardLink(fseffect.HardLinkRequest{
				Source:   fseffect.Location{Root: host.retainedDir(), Rel: before.RetainedSlot},
				Location: fseffect.Location{Root: rootDir, Rel: target.Path.Rel},
				Replace:  true,
			}); err != nil {
				return err
			}
			return restoreTargetFileMetadata(root, target.Path.Rel, before.Mode, before.Xattrs)
		}
		if before.HardLinkGroup != "" {
			if peerRel, pErr := host.findHardLinkPeer(root, target.Path.Rel, before); pErr != nil {
				return pErr
			} else if peerRel != "" {
				if err := fseffect.HardLink(fseffect.HardLinkRequest{
					Source:   fseffect.Location{Root: rootDir, Rel: peerRel},
					Location: fseffect.Location{Root: rootDir, Rel: target.Path.Rel},
					Replace:  true,
				}); err != nil {
					return err
				}
				return restoreTargetFileMetadata(root, target.Path.Rel, before.Mode, before.Xattrs)
			}
		}
		change := PlannedChange{
			Path:         target.Path,
			Action:       ActionWriteRegular,
			Mode:         before.Mode,
			MergedSHA256: before.SHA256,
			Xattrs:       before.Xattrs,
		}
		if err := host.targets.writeTargetRegular(root, change); err != nil {
			return err
		}
		return restoreTargetFileMetadata(root, target.Path.Rel, before.Mode, before.Xattrs)
	})
}

func (host *HostPromotion) restoreHostHardLinks(targets []hostTargetUndo) error {
	for _, target := range targets {
		source := target.Before.HardLinkSource
		if source == nil {
			continue
		}
		current, err := host.captureHostTarget(target.Path)
		if err != nil {
			return err
		}
		if current.Kind != layeredfs.EntryAbsent && !sameHostTarget(current, target.Before) && !sameHostTarget(current, target.After) {
			return fmt.Errorf("checkpoint: hard-link target diverged during recovery: %s", pathKey(target.Path))
		}
		if sameHostTarget(current, target.Before) && host.targetMatchesBeforeInode(target, current) {
			continue
		}
		rootDir := host.targets.roots[target.Path.RootID]
		root, err := host.targets.openTargetRoot(target.Path.RootID)
		if err != nil {
			return err
		}
		err = withWritableParent(root, target.Path.Rel, func() error {
			if err := host.targets.ensureTargetParents(root, target.Path); err != nil {
				return err
			}
			srcLoc := fseffect.Location{Root: rootDir, Rel: source.Rel}
			if target.Before.RetainedSlot != "" {
				retained, rErr := host.captureRetainedTarget(target.Before.RetainedSlot)
				if rErr != nil {
					return fmt.Errorf("checkpoint: missing retained inode slot %s for %s: %w", target.Before.RetainedSlot, pathKey(target.Path), rErr)
				}
				if !sameHostTarget(retained, target.Before) || (target.Before.HardLinkGroup != "" && retained.identity != target.Before.HardLinkGroup) {
					return fmt.Errorf("checkpoint: retained inode diverged for %s", pathKey(target.Path))
				}
				srcLoc = fseffect.Location{Root: host.retainedDir(), Rel: target.Before.RetainedSlot}
			}
			if err := fseffect.HardLink(fseffect.HardLinkRequest{
				Source:   srcLoc,
				Location: fseffect.Location{Root: rootDir, Rel: target.Path.Rel},
				Replace:  true,
			}); err != nil {
				return err
			}
			return restoreTargetFileMetadata(root, target.Path.Rel, target.Before.Mode, target.Before.Xattrs)
		})
		_ = root.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func (host *HostPromotion) findHardLinkPeer(root *os.Root, skipRel string, want hostTargetImage) (string, error) {
	var peer string
	err := fs.WalkDir(root.FS(), ".", func(rel string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || rel == "." || rel == skipRel || entry.IsDir() || !entry.Type().IsRegular() {
			return nil
		}
		file, openErr := root.Open(rel)
		if openErr != nil {
			return nil
		}
		info, statErr := file.Stat()
		id, _, idErr := targetFileIdentityAndLinks(file, info)
		_ = file.Close()
		if statErr == nil && idErr == nil && id == want.HardLinkGroup {
			peer = filepath.Clean(rel)
			return fs.SkipAll
		}
		return nil
	})
	return peer, err
}
