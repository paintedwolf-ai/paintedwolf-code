//go:build linux

package launchguard

import (
	"errors"
	"fmt"
	"os"
	"sort"

	"golang.org/x/sys/unix"
)

type moduleCodeRoot struct {
	path   string
	object *os.File
	tree   *os.File
}

// Retain each exact policy object before projection. A later parent overlay
// must not change the source selected for a separate descendant policy root.
func captureModuleCodeRoots(policy GenerationPolicy, desktop *os.File) (_ []moduleCodeRoot, err error) {
	interpreter, err := programInterpreter(desktop)
	if err != nil {
		return nil, err
	}
	types := make(map[string]bool)
	for _, name := range policy.LibraryRoots {
		types[name] = true
	}
	for _, name := range policy.SystemExecutables {
		if types[name] {
			return nil, errors.New("module policy has conflicting file and directory roles")
		}
		types[name] = false
	}
	if interpreter != "" {
		if types[interpreter] {
			return nil, errors.New("module interpreter conflicts with a directory role")
		}
		types[interpreter] = false
	}
	var roots []moduleCodeRoot
	defer func() {
		if err != nil {
			closeModuleCodeRoots(roots)
		}
	}()
	for name, directory := range types {
		object, openErr := openRootControlled(name, directory)
		if openErr != nil {
			return nil, fmt.Errorf("retain module policy object: %w", openErr)
		}
		roots = append(roots, moduleCodeRoot{path: name, object: object})
		if err := roots[len(roots)-1].captureTree(); err != nil {
			return nil, err
		}
	}
	sort.Slice(roots, func(a, b int) bool { return roots[a].path < roots[b].path })
	generation, err := openRootControlled(policy.LogicalInstallRoot, true)
	if err != nil {
		return nil, err
	}
	// Restore the held generation after any containing library projection.
	roots = append(roots, moduleCodeRoot{path: policy.LogicalInstallRoot, object: generation})
	if err := roots[len(roots)-1].captureTree(); err != nil {
		return nil, err
	}
	return roots, nil
}

// Clone while the selected object still belongs to the current namespace.
// The detached tree can then be moved after private staging is established.
func (root *moduleCodeRoot) captureTree() error {
	tree, err := unix.OpenTree(int(root.object.Fd()), "", unix.OPEN_TREE_CLONE|unix.OPEN_TREE_CLOEXEC|unix.AT_EMPTY_PATH)
	if err != nil {
		return fmt.Errorf("clone selected module object %q: %w", root.path, err)
	}
	root.tree = os.NewFile(uintptr(tree), "selected-module-projection")
	return nil
}

func closeModuleCodeRoots(roots []moduleCodeRoot) {
	for _, root := range roots {
		_ = root.object.Close()
		if root.tree != nil {
			_ = root.tree.Close()
		}
	}
}

func projectModuleCodeRoots(roots []moduleCodeRoot) error {
	if err := unix.MountSetattr(unix.AT_FDCWD, "/", unix.AT_RECURSIVE, &unix.MountAttr{Attr_set: unix.MOUNT_ATTR_NOEXEC}); err != nil {
		return err
	}
	for _, root := range roots {
		if err := projectModuleCodeRoot(root); err != nil {
			return fmt.Errorf("project selected module root %q: %w", root.path, err)
		}
	}
	return nil
}

func projectModuleCodeRoot(root moduleCodeRoot) error {
	// Single-mount cloning preserves the selected object and omits every
	// descendant mount. Initial volumes have their own detached safe leases.
	tree := int(root.tree.Fd())
	if err := unix.MountSetattr(tree, "", unix.AT_EMPTY_PATH, &unix.MountAttr{Attr_clr: unix.MOUNT_ATTR_NOEXEC, Propagation: unix.MS_PRIVATE}); err != nil {
		return fmt.Errorf("prepare executable projection: %w", err)
	}
	target, err := openMountPoint(root.path)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(target) }()
	if err := unix.MoveMount(tree, "", target, "", unix.MOVE_MOUNT_F_EMPTY_PATH|unix.MOVE_MOUNT_T_EMPTY_PATH); err != nil {
		return fmt.Errorf("attach executable projection: %w", err)
	}
	if alias, err := os.Readlink(mountDescriptorPath(tree)); err != nil || alias != root.path {
		return errors.New("selected module projection moved during placement")
	}
	return nil
}

// Compare the retained selected object with the staging alias before any
// projection; source-key correspondence alone is never an object witness.
func verifyModuleCodeRoots(roots []moduleCodeRoot) error {
	for _, root := range roots {
		current, err := openMountPoint(root.path)
		if err != nil {
			return err
		}
		var selected, actual unix.Stat_t
		err = unix.Fstat(int(root.object.Fd()), &selected)
		if err == nil {
			err = unix.Fstat(current, &actual)
		}
		_ = unix.Close(current)
		if err != nil {
			return err
		}
		if selected.Dev != actual.Dev || selected.Ino != actual.Ino || selected.Mode != actual.Mode || selected.Uid != actual.Uid || selected.Gid != actual.Gid {
			return errors.New("module root changed from retained source selection")
		}
	}
	return nil
}
