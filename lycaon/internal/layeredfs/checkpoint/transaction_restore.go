package checkpoint

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/layeredfs"
)

func (txn *Transaction) retainTargetInode(
	path layeredfs.QualifiedPath,
	image targetImage,
	idx int,
	after map[layeredfs.QualifiedPath]targetImage,
) (string, error) {
	rootDir := txn.targets.roots[path.RootID]
	if rootDir == "" {
		return "", layeredfs.ErrInvalidRoot
	}
	slot := targetSlot(idx)
	err := txn.cfg.RetainLink(fseffect.HardLinkRequest{
		Source:   fseffect.Location{Root: rootDir, Rel: path.Rel},
		Location: fseffect.Location{Root: txn.dir, Rel: filepath.Join("retained", slot)},
		DirMode:  0o700,
		Replace:  true,
	})
	if fseffect.HardLinkUnavailable(err) {
		// A lone name is restorable from its before-image blob; aliases need the inode.
		if image.links <= 1 || txn.hasSurvivingInRootPeer(path.RootID, image.identity, after) {
			return "", nil
		}
		return "", fmt.Errorf("checkpoint: cannot retain hard-linked target inode for %s: %w", pathKey(path), err)
	}
	if err != nil {
		return "", err
	}
	retained, rErr := txn.captureRetainedTarget(slot)
	if rErr != nil {
		_ = os.Remove(filepath.Join(txn.retainedDir(), slot))
		return "", rErr
	}
	if retained.identity != image.identity || !sameTarget(retained, image) {
		_ = os.Remove(filepath.Join(txn.retainedDir(), slot))
		return "", ErrStalePreviewTargetChanged
	}
	return slot, nil
}

func (txn *Transaction) hasSurvivingInRootPeer(rootID, identity string, after map[layeredfs.QualifiedPath]targetImage) bool {
	if identity == "" {
		return false
	}
	root, err := txn.targets.openTargetRoot(rootID)
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

// recordedGroupMatches compares a journaled inode with a live one, which a restart
// may have moved to a different device number.
func recordedGroupMatches(group, live string) bool {
	return group == "" || fspath.SameEntryAcrossRestart(group, live)
}

func (txn *Transaction) validateRetainedSlot(path layeredfs.QualifiedPath, before targetImage) error {
	if before.Kind != layeredfs.EntryRegular || before.RetainedSlot == "" {
		return nil
	}
	retained, err := txn.captureRetainedTarget(before.RetainedSlot)
	if err != nil {
		return fmt.Errorf("checkpoint: missing retained inode slot %s for %s: %w", before.RetainedSlot, pathKey(path), err)
	}
	if !sameTarget(retained, before) || !recordedGroupMatches(before.HardLinkGroup, retained.identity) {
		return fmt.Errorf("checkpoint: cannot recover diverged retained inode for %s", pathKey(path))
	}
	return nil
}

func (txn *Transaction) captureRetainedTarget(slot string) (targetImage, error) {
	if !safeComponent(slot) {
		return targetImage{}, ErrInvalidManifest
	}
	p := filepath.Join(txn.retainedDir(), slot)
	file, err := os.Open(p)
	if err != nil {
		return targetImage{}, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return targetImage{}, err
	}
	if !info.Mode().IsRegular() {
		return targetImage{}, ErrInvalidManifest
	}
	id, links, err := targetFileIdentityAndLinks(file, info)
	if err != nil {
		return targetImage{}, err
	}
	attrs, err := readFileXattrs(file)
	if err != nil {
		return targetImage{}, err
	}
	body, err := io.ReadAll(file)
	if err != nil {
		return targetImage{}, err
	}
	return targetImage{
		Kind:     layeredfs.EntryRegular,
		Mode:     info.Mode().Perm(),
		SHA256:   sha256Hex(body),
		Size:     info.Size(),
		Xattrs:   attrs,
		identity: id,
		links:    links,
	}, nil
}

func (txn *Transaction) targetMatchesBeforeInode(target targetUndo, current targetImage) bool {
	before := target.Before
	if before.Kind != layeredfs.EntryRegular {
		return true
	}
	if before.RetainedSlot != "" {
		retained, err := txn.captureRetainedTarget(before.RetainedSlot)
		if err != nil {
			return false
		}
		return current.identity != "" && current.identity == retained.identity && recordedGroupMatches(before.HardLinkGroup, retained.identity)
	}
	if before.HardLinkSource != nil {
		if srcImg, err := txn.captureTarget(*before.HardLinkSource); err == nil {
			return current.identity != "" && current.identity == srcImg.identity
		}
		return false
	}
	if before.HardLinkGroup != "" {
		return recordedGroupMatches(before.HardLinkGroup, current.identity)
	}
	return true
}

func (txn *Transaction) restoreRegularTarget(target targetUndo) error {
	before := target.Before
	current, err := txn.captureTarget(target.Path)
	if err != nil {
		return err
	}
	if sameTarget(current, before) && txn.targetMatchesBeforeInode(target, current) {
		return nil
	}
	rootDir := txn.targets.roots[target.Path.RootID]
	if rootDir == "" {
		return layeredfs.ErrInvalidRoot
	}
	root, err := txn.targets.openTargetRoot(target.Path.RootID)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()

	return withWritableParent(root, target.Path.Rel, func() error {
		if err := txn.targets.ensureTargetParents(root, target.Path); err != nil {
			return err
		}
		if before.RetainedSlot != "" {
			retained, rErr := txn.captureRetainedTarget(before.RetainedSlot)
			if rErr != nil {
				return fmt.Errorf("checkpoint: missing retained inode slot %s for %s: %w", before.RetainedSlot, pathKey(target.Path), rErr)
			}
			if !sameTarget(retained, before) || !recordedGroupMatches(before.HardLinkGroup, retained.identity) {
				return fmt.Errorf("checkpoint: retained inode diverged for %s", pathKey(target.Path))
			}
			if err := fseffect.HardLink(fseffect.HardLinkRequest{
				Source:   fseffect.Location{Root: txn.retainedDir(), Rel: before.RetainedSlot},
				Location: fseffect.Location{Root: rootDir, Rel: target.Path.Rel},
				Replace:  true,
			}); err != nil {
				return err
			}
			return restoreTargetFileMetadata(root, target.Path.Rel, before.Mode, before.Xattrs)
		}
		if before.HardLinkGroup != "" {
			if peerRel, pErr := txn.findHardLinkPeer(root, target.Path.Rel, before); pErr != nil {
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
		original, err := txn.readOriginal(before.SHA256)
		if err != nil {
			return err
		}
		change := PlannedChange{
			Path:        target.Path,
			Action:      ActionWriteRegular,
			Mode:        before.Mode,
			MergedBytes: original,
			Xattrs:      before.Xattrs,
		}
		if err := txn.targets.writeTargetRegular(root, change); err != nil {
			return err
		}
		return restoreTargetFileMetadata(root, target.Path.Rel, before.Mode, before.Xattrs)
	})
}

func (txn *Transaction) restoreHardLinks(targets []targetUndo) error {
	for _, target := range targets {
		source := target.Before.HardLinkSource
		if source == nil {
			continue
		}
		current, err := txn.captureTarget(target.Path)
		if err != nil {
			return err
		}
		if current.Kind != layeredfs.EntryAbsent && !sameTarget(current, target.Before) && !sameTarget(current, target.After) {
			return fmt.Errorf("checkpoint: hard-link target diverged during recovery: %s", pathKey(target.Path))
		}
		if sameTarget(current, target.Before) && txn.targetMatchesBeforeInode(target, current) {
			continue
		}
		rootDir := txn.targets.roots[target.Path.RootID]
		root, err := txn.targets.openTargetRoot(target.Path.RootID)
		if err != nil {
			return err
		}
		err = withWritableParent(root, target.Path.Rel, func() error {
			if err := txn.targets.ensureTargetParents(root, target.Path); err != nil {
				return err
			}
			srcLoc := fseffect.Location{Root: rootDir, Rel: source.Rel}
			if target.Before.RetainedSlot != "" {
				retained, rErr := txn.captureRetainedTarget(target.Before.RetainedSlot)
				if rErr != nil {
					return fmt.Errorf("checkpoint: missing retained inode slot %s for %s: %w", target.Before.RetainedSlot, pathKey(target.Path), rErr)
				}
				if !sameTarget(retained, target.Before) || !recordedGroupMatches(target.Before.HardLinkGroup, retained.identity) {
					return fmt.Errorf("checkpoint: retained inode diverged for %s", pathKey(target.Path))
				}
				srcLoc = fseffect.Location{Root: txn.retainedDir(), Rel: target.Before.RetainedSlot}
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

func (txn *Transaction) findHardLinkPeer(root *os.Root, skipRel string, want targetImage) (string, error) {
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
		if statErr == nil && idErr == nil && recordedGroupMatches(want.HardLinkGroup, id) {
			peer = filepath.Clean(rel)
			return fs.SkipAll
		}
		return nil
	})
	return peer, err
}

func (txn *Transaction) readOriginal(sha string) ([]byte, error) {
	if !safeComponent(sha) {
		return nil, ErrInvalidManifest
	}
	body, err := os.ReadFile(filepath.Join(txn.originalsDir(), sha))
	if err != nil {
		return nil, fmt.Errorf("checkpoint: original bytes unavailable: %w", err)
	}
	if sha256Hex(body) != sha {
		return nil, fmt.Errorf("%w: original bytes diverged", ErrCorruptBlob)
	}
	if body == nil {
		body = []byte{}
	}
	return body, nil
}

func (txn *Transaction) removeTargetEntry(path layeredfs.QualifiedPath) error {
	root, err := txn.targets.openTargetRoot(path.RootID)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	return withWritableParent(root, path.Rel, func() error {
		if err := root.Remove(path.Rel); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	})
}

func (txn *Transaction) finalizeRestoredDirectories(shallowToDeep []targetUndo) error {
	for i := len(shallowToDeep) - 1; i >= 0; i-- {
		target := shallowToDeep[i]
		if target.Before.Kind != layeredfs.EntryDirectory {
			continue
		}
		root, err := txn.targets.openTargetRoot(target.Path.RootID)
		if err != nil {
			return err
		}
		err = restoreTargetFileMetadata(root, target.Path.Rel, target.Before.Mode, target.Before.Xattrs)
		_ = root.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func restoreTargetFileMetadata(root *os.Root, rel string, mode os.FileMode, xattrs map[string][]byte) error {
	file, err := root.Open(rel)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	if info, sErr := file.Stat(); sErr == nil && info.Mode().Perm()&0o200 == 0 {
		_ = file.Chmod(info.Mode().Perm() | 0o200)
	}
	if err := restoreFileXattrs(file, xattrs); err != nil {
		return err
	}
	if mode != 0 {
		if err := file.Chmod(mode.Perm()); err != nil {
			return err
		}
	}
	return syncFile(file)
}

func withWritableParent(root *os.Root, rel string, fn func() error) error {
	var ancestors []string
	for dir := filepath.Dir(rel); dir != "."; dir = filepath.Dir(dir) {
		ancestors = append(ancestors, dir)
	}
	slices.Reverse(ancestors)
	type restoredPerm struct {
		dir  string
		mode os.FileMode
	}
	var restore []restoredPerm
	for _, dir := range ancestors {
		info, err := root.Lstat(dir)
		if err != nil || !info.IsDir() {
			continue
		}
		perm := info.Mode().Perm()
		if perm&0o700 != 0o700 {
			if f, openErr := root.Open(dir); openErr == nil {
				if cErr := f.Chmod(perm | 0o700); cErr == nil {
					restore = append(restore, restoredPerm{dir: dir, mode: perm})
				}
				_ = f.Close()
			}
		}
	}
	defer func() {
		for i := len(restore) - 1; i >= 0; i-- {
			if f, openErr := root.Open(restore[i].dir); openErr == nil {
				_ = f.Chmod(restore[i].mode)
				_ = syncFile(f)
				_ = f.Close()
			}
		}
	}()
	return fn()
}
