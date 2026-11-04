package checkpoint

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/lycaon/lycaon/internal/fssync"
)

func (tree *promotionTargets) applyPlannedChange(ch PlannedChange) error {
	root, err := tree.openTargetRoot(ch.Path.RootID)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	return withWritableParent(root, ch.Path.Rel, func() error {
		return tree.applyPlannedChangeInRoot(root, ch)
	})
}

func (tree *promotionTargets) applyPlannedChangeInRoot(root *os.Root, ch PlannedChange) error {
	switch ch.Action {
	case ActionNoop:
		return nil
	case ActionDelete:
		return root.RemoveAll(ch.Path.Rel)
	case ActionWriteRegular, ActionMkdir, ActionSymlink, ActionHardLink:
		// These actions retain the current namespace until publication below.
	case ActionReplaceWithDirectory, ActionReplaceDirWithRegular:
		if err := root.RemoveAll(ch.Path.Rel); err != nil {
			return err
		}
	}
	switch ch.Action {
	case ActionMkdir, ActionReplaceWithDirectory:
		return tree.makeTargetDirectory(root, ch.Path, pickMode(ch.Mode, 0755).Perm(), ch.Xattrs)
	case ActionWriteRegular, ActionReplaceDirWithRegular:
		return tree.writeTargetRegular(root, ch)
	case ActionSymlink, ActionHardLink:
		if err := tree.ensureTargetParents(root, ch.Path); err != nil {
			return err
		}
		if ch.Action == ActionHardLink && (ch.HardLinkSource == nil || ch.HardLinkSource.RootID != ch.Path.RootID) {
			return syscall.EXDEV
		}
		if ch.Action == ActionHardLink {
			source, err := root.Open(ch.HardLinkSource.Rel)
			if err != nil {
				return err
			}
			if tree.recordPostimage != nil {
				err = tree.recordPostimage(ch.Path, source)
			}
			_ = source.Close()
			if err != nil {
				return err
			}
		}
		if err := root.RemoveAll(ch.Path.Rel); err != nil {
			return err
		}
		if ch.Action == ActionSymlink {
			return root.Symlink(ch.LinkTarget, ch.Path.Rel)
		}
		return root.Link(ch.HardLinkSource.Rel, ch.Path.Rel)
	default:
		return fmt.Errorf("checkpoint: unsupported action %q", ch.Action)
	}
}

func (tree *promotionTargets) writeTargetRegular(root *os.Root, ch PlannedChange) error {
	data := ch.MergedBytes
	if data == nil && ch.MergedSHA256 != "" {
		var err error
		data, err = tree.store.ReadBlob(ch.MergedSHA256)
		if err != nil {
			return err
		}
	}
	parent := filepath.Dir(ch.Path.Rel)
	if err := tree.ensureTargetParents(root, ch.Path); err != nil {
		return err
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return err
	}
	temp := filepath.Join(parent, ".checkpoint-"+hex.EncodeToString(random[:]))
	mode := pickMode(ch.Mode, 0644).Perm()
	file, err := root.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	identity, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return err
	}
	published := false
	defer func() {
		_ = file.Close()
		if !published {
			if current, err := root.Lstat(temp); err == nil && os.SameFile(identity, current) {
				_ = root.Remove(temp)
			}
		}
	}()
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := applyFileXattrs(file, ch.Xattrs); err != nil {
		return err
	}
	if err := file.Chmod(mode); err != nil {
		return err
	}
	if tree.recordPostimage != nil {
		if err := tree.recordPostimage(ch.Path, file); err != nil {
			return err
		}
	}
	if err := fssync.File(file); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	current, err := root.Lstat(temp)
	if err != nil {
		return err
	}
	if !os.SameFile(identity, current) {
		return ErrStalePreviewTargetChanged
	}
	if err := root.Rename(temp, ch.Path.Rel); err != nil {
		return err
	}
	published = true
	dir, err := root.Open(parent)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	return fssync.File(dir)
}
