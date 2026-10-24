//go:build linux

package launchguard

import (
	"errors"
	"fmt"
	"os"
	"strconv"

	"golang.org/x/sys/unix"
)

// Clone a single mount, not its descendants: each nested source mount has its
// own lease and removal. A recursive clone would import untracked child mounts.
func prepareMountAttachment(source int) (tree int, err error) {
	tree, err = unix.OpenTree(source, "", unix.OPEN_TREE_CLONE|unix.OPEN_TREE_CLOEXEC|unix.AT_EMPTY_PATH)
	if err != nil {
		return -1, err
	}
	if err = lockDetachedMount(tree); err != nil {
		_ = unix.Close(tree)
		return -1, err
	}
	return tree, nil
}

func lockDetachedMount(tree int) error {
	return unix.MountSetattr(tree, "", unix.AT_EMPTY_PATH|unix.AT_RECURSIVE, &unix.MountAttr{
		Attr_set:    unix.MOUNT_ATTR_NOEXEC | unix.MOUNT_ATTR_NOSUID | unix.MOUNT_ATTR_NODEV,
		Propagation: unix.MS_PRIVATE,
	})
}

type mountedVolume struct {
	lease   uint64
	path    string
	tree    int
	mountID uint64
}

func attachPreparedMount(lease uint64, name string, tree int) (_ *mountedVolume, err error) {
	// Defense at the final visibility boundary: a transfer never authorizes
	// executable flags, nor receive-only propagation from the source tree.
	if err := lockDetachedMount(tree); err != nil {
		return nil, err
	}
	target, err := openMountPoint(name)
	if err != nil {
		return nil, &mountDestinationError{err: err}
	}
	defer func() { _ = unix.Close(target) }()
	id, err := mountID(tree)
	if err != nil {
		return nil, err
	}
	if err := unix.MoveMount(tree, "", target, "", unix.MOVE_MOUNT_F_EMPTY_PATH|unix.MOVE_MOUNT_T_EMPTY_PATH); err != nil {
		return nil, err
	}
	return confirmMountAttachment(lease, name, tree, id)
}

// Confirmation owns rollback after the move, but its caller retains the tree
// descriptor on failure. On success the returned volume owns it until detach.
func confirmMountAttachment(lease uint64, name string, tree int, id uint64) (_ *mountedVolume, err error) {
	mounted := &mountedVolume{lease: lease, path: name, tree: tree, mountID: id}
	defer func() {
		if err != nil {
			// The startup stage and the watcher never mutate M_view at the
			// same time. Even on refusal, check the exact visible mount before
			// unmounting; a proc-fd pathname alone is not mount authority.
			err = errors.Join(err, mounted.unmount())
		}
	}()
	// A pinned target can be renamed during lookup. Never claim that a stale
	// lexical alias is the attachment location; retire the prepared tree if
	// that race is observed. The kernel always binds the opened object.
	if actual, linkErr := os.Readlink(mountDescriptorPath(tree)); linkErr != nil || actual != name {
		return nil, errors.New("attached mount point moved during attachment")
	}
	return mounted, nil
}

func mountDescriptorPath(fd int) string {
	return "/proc/thread-self/fd/" + strconv.Itoa(fd)
}

// Mount operations are serialized and replacements retire the old lease
// before attachment. A proc-fd unmount follows covering mounts, so refuse if
// the retained root is no longer visible. Its current kernel-reported alias,
// rather than its original name, follows ordinary directory renames.
func (volume *mountedVolume) detach() error {
	fd := volume.tree
	if err := volume.unmount(); err != nil {
		return err
	}
	if fd >= 0 {
		volume.tree = -1
		_ = unix.Close(fd)
	}
	return nil
}

// Failure before attachment returns ownership of the descriptor to its caller.
// Post-move refusal unmounts the tree without closing that caller-owned FD.
func (volume *mountedVolume) unmount() error {
	if volume.tree < 0 {
		return nil
	}
	fd := volume.tree
	id, err := mountID(fd)
	if err != nil || id != volume.mountID {
		return errors.New("attached mount identity changed")
	}
	current, err := os.Readlink(mountDescriptorPath(fd))
	if err != nil {
		return err
	}
	visible, err := openMountPoint(current)
	if err != nil {
		return err
	}
	visibleID, statErr := mountID(visible)
	_ = unix.Close(visible)
	if statErr != nil || visibleID != volume.mountID {
		return errors.New("retained volume is covered or no longer visible")
	}
	if err := unix.Unmount(mountDescriptorPath(fd), unix.MNT_DETACH); err != nil {
		return fmt.Errorf("detach retained volume: %w", err)
	}
	return nil
}

// A failed target lookup changes no namespace state and permits only refusal,
// never a fallback path. Other attachment/identity errors remain fatal.
type mountDestinationError struct{ err error }

func (failure *mountDestinationError) Error() string {
	return "module mount destination is unavailable: " + failure.err.Error()
}
func (failure *mountDestinationError) Unwrap() error { return failure.err }
