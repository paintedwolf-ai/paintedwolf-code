//go:build linux

package launchguard

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// Runs only inside the root fixture's private mount namespace. A deterministic
// rename after the actual move exercises the same confirmation/rollback seam
// as a concurrent rename between move_mount and attachment confirmation.
func checkAttachmentRollbackOwnership(directory string) error {
	base := filepath.Join(directory, "attachment-rollback")
	if err := os.Mkdir(base, 0o755); err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(base) }()
	source := filepath.Join(base, "source")
	parent := filepath.Join(base, "parent")
	for _, name := range []string{source, parent, filepath.Join(parent, "target")} {
		if err := os.Mkdir(name, 0o755); err != nil {
			return err
		}
	}
	if err := unix.Mount("tmpfs", source, "tmpfs", 0, "mode=0755,size=1m"); err != nil {
		return err
	}
	defer func() { _ = unix.Unmount(source, unix.MNT_DETACH) }()
	root, err := openMountPoint(source)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(root) }()
	tree, err := prepareMountAttachment(root)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(tree) }()
	id, err := mountID(tree)
	if err != nil {
		return err
	}
	alias := filepath.Join(parent, "target")
	target, err := openMountPoint(alias)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(target) }()
	if err := unix.MoveMount(tree, "", target, "", unix.MOVE_MOUNT_F_EMPTY_PATH|unix.MOVE_MOUNT_T_EMPTY_PATH); err != nil {
		return err
	}
	moved := filepath.Join(base, "moved")
	if err := os.Rename(parent, moved); err != nil {
		return err
	}
	volume, confirmErr := confirmMountAttachment(1, alias, tree, id)
	if confirmErr == nil || volume != nil {
		return errors.New("attachment accepted a stale alias after move")
	}
	if _, err := unix.FcntlInt(uintptr(tree), unix.F_GETFD, 0); err != nil {
		return errors.New("attachment rollback consumed its caller-owned descriptor")
	}
	visible, err := openMountPoint(filepath.Join(moved, "target"))
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(visible) }()
	visibleID, err := mountID(visible)
	if err != nil || visibleID == id {
		return errors.New("attachment refusal retained its moved mount")
	}
	return nil
}
