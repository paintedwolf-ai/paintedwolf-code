//go:build linux

package launchguard

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// enterModuleView gives the calling thread, and every process it then starts,
// a mount namespace of its own in which native code maps executable only from
// the administrator's closure: the selected generation, the desktop image's
// program interpreter, the policy's system executables and its library roots.
// Every other mount, including each user-writable one, is noexec, so the
// dynamic loader cannot map a module from a user-controlled path, however a
// library names it. The mounts are owned by the initial user namespace, so
// nothing inside the protected namespace can clear the attribute.
//
// The broker agent keeps the launch's original mount namespace: programs it
// starts outside the protected namespace keep the caller's ordinary authority,
// including executing from the caller's own files. leave returns the thread
// there. The caller holds the thread locked from enterModuleView until leave.
func enterModuleView(policy GenerationPolicy, image *os.File) (leave func() error, err error) {
	interpreter, err := programInterpreter(image)
	if err != nil {
		return nil, err
	}
	original, err := os.Open("/proc/thread-self/ns/mnt")
	if err != nil {
		return nil, err
	}
	leave = func() error {
		defer func() { _ = original.Close() }()
		return unix.Setns(int(original.Fd()), unix.CLONE_NEWNS)
	}
	if err := unix.Unshare(unix.CLONE_NEWNS); err != nil {
		_ = original.Close()
		return nil, fmt.Errorf("module view namespace: %w", err)
	}
	if err := applyModuleView(policy, interpreter); err != nil {
		_ = leave()
		return nil, err
	}
	return leave, nil
}

// moduleRoots is every root-controlled path the module view leaves executable
// beside the generation.
func moduleRoots(policy GenerationPolicy, interpreter string) []string {
	roots := append([]string{}, policy.LibraryRoots...)
	roots = append(roots, policy.SystemExecutables...)
	if interpreter != "" {
		roots = append(roots, interpreter)
	}
	return roots
}

func applyModuleView(policy GenerationPolicy, interpreter string) error {
	noexec := unix.MountAttr{Attr_set: unix.MOUNT_ATTR_NOEXEC}
	if err := unix.MountSetattr(unix.AT_FDCWD, "/", unix.AT_RECURSIVE, &noexec); err != nil {
		return fmt.Errorf("mark mounts noexec: %w", err)
	}
	for _, root := range moduleRoots(policy, interpreter) {
		if err := allowModules(root); err != nil {
			return err
		}
	}
	// The generation is restored last, so a library root above the logical
	// installation root cannot cover it with a noexec copy.
	exec := unix.MountAttr{Attr_clr: unix.MOUNT_ATTR_NOEXEC}
	if err := unix.MountSetattr(unix.AT_FDCWD, policy.LogicalInstallRoot, unix.AT_RECURSIVE, &exec); err != nil {
		return fmt.Errorf("allow the generation's modules: %w", err)
	}
	return nil
}

// allowModules places an executable copy of one root-controlled file or
// directory over itself. Mounts beneath it keep noexec: only the filesystem
// the administrator named is trusted, not whatever is later mounted inside it.
func allowModules(path string) error {
	directory := true
	if info, err := os.Lstat(path); err == nil {
		directory = info.IsDir()
	}
	object, err := openRootControlled(path, directory)
	if err != nil {
		return fmt.Errorf("module root %s: %w", path, err)
	}
	defer func() { _ = object.Close() }()
	tree, err := unix.OpenTree(int(object.Fd()), "", unix.OPEN_TREE_CLONE|unix.OPEN_TREE_CLOEXEC|unix.AT_EMPTY_PATH|unix.AT_RECURSIVE)
	if err != nil {
		return fmt.Errorf("copy module root %s: %w", path, err)
	}
	defer func() { _ = unix.Close(tree) }()
	exec := unix.MountAttr{Attr_clr: unix.MOUNT_ATTR_NOEXEC}
	if err := unix.MountSetattr(tree, "", unix.AT_EMPTY_PATH, &exec); err != nil {
		return fmt.Errorf("allow modules from %s: %w", path, err)
	}
	if err := unix.MoveMount(tree, "", int(object.Fd()), "", unix.MOVE_MOUNT_F_EMPTY_PATH|unix.MOVE_MOUNT_T_EMPTY_PATH); err != nil {
		return fmt.Errorf("place module root %s: %w", path, err)
	}
	return nil
}

// reopenImage opens the desktop image again inside the module view, where the
// entry executes it, and proves it is the inode the stage selected.
func reopenImage(policy GenerationPolicy, selected *os.File) (*os.File, error) {
	reopened, err := os.Open(filepath.Join(policy.LogicalInstallRoot, "bin/painted-wolf-code"))
	if err != nil {
		return nil, err
	}
	var a, b unix.Stat_t
	if err := unix.Fstat(int(selected.Fd()), &a); err != nil {
		_ = reopened.Close()
		return nil, err
	}
	if err := unix.Fstat(int(reopened.Fd()), &b); err != nil || a.Dev != b.Dev || a.Ino != b.Ino {
		_ = reopened.Close()
		return nil, errors.New("module view image differs from retained selection")
	}
	return reopened, nil
}
