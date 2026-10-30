//go:build linux

package launchguard

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// Runs inside the preparation fixture's private initial-root mount namespace.
func checkAdministrativePruning() error {
	root, err := os.MkdirTemp("/run", "pw-mount-prune-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(root) }()
	for _, name := range []string{"lower", "upper", "work", "overlay"} {
		if err := os.Mkdir(filepath.Join(root, name), 0755); err != nil {
			return err
		}
	}
	if err := os.WriteFile(filepath.Join(root, "lower", "payload"), []byte("administrative overlay"), 0644); err != nil {
		return err
	}
	overlay := filepath.Join(root, "overlay")
	options := "lowerdir=" + filepath.Join(root, "lower") + ",upperdir=" + filepath.Join(root, "upper") + ",workdir=" + filepath.Join(root, "work")
	if err := unix.Mount("overlay", overlay, "overlay", 0, options); err != nil {
		return err
	}
	defer func() { _ = unix.Unmount(overlay, unix.MNT_DETACH) }()
	namespace := filepath.Join(root, "namespace")
	if err := os.WriteFile(namespace, nil, 0600); err != nil {
		return err
	}
	if err := unix.Mount("/proc/self/ns/net", namespace, "", unix.MS_BIND, ""); err != nil {
		return err
	}
	defer func() { _ = unix.Unmount(namespace, unix.MNT_DETACH) }()
	for _, target := range []string{overlay, namespace} {
		_, before, err := readCurrentMountTopology()
		if err != nil {
			return err
		}
		var selected mountEntry
		for _, entry := range before {
			if entry.mountPoint == target {
				selected = entry
			}
		}
		if selected.id == 0 {
			return errors.New("administrative fixture mount missing")
		}
		if err := os.Chmod(root, 0777); err != nil {
			return err
		}
		refused := pruneAdministrativeMount(selected, before)
		if err := os.Chmod(root, 0700); err != nil {
			return err
		}
		if refused == nil {
			return errors.New("prune admitted caller-writable parent")
		}
		_, unchanged, err := readCurrentMountTopology()
		if err != nil || !sameMountEpoch(before, unchanged, nil) {
			return errors.New("refused prune changed topology")
		}
		if err := pruneAdministrativeMount(selected, before); err != nil {
			return err
		}
		_, after, err := readCurrentMountTopology()
		if err != nil || !sameMountEpoch(before, after, map[int]bool{selected.id: true}) {
			return errors.New("administrative prune changed unexpected topology")
		}
	}
	return nil
}
