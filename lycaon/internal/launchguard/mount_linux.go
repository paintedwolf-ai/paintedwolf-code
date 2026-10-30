//go:build linux

package launchguard

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"golang.org/x/sys/unix"
)

// bindGeneration runs in the privileged stage, as uid 0 of the initial user
// namespace, inside the fresh mount namespace it was cloned into. It binds the
// retained generation read-only over the logical installation root and
// reopens the desktop image through that logical name, so its executable and
// mappings resolve there, while proving the inode is the one selected.
func bindGeneration(policy GenerationPolicy, generation, desktop *os.File) (*os.File, error) {
	info, err := generation.Stat()
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, errors.New("selected generation is not a directory")
	}
	if err := trustedObject(info); err != nil {
		return nil, err
	}
	target, err := openRootControlled(policy.LogicalInstallRoot, true)
	if err != nil {
		return nil, err
	}
	_ = target.Close()
	// A bind source must belong to this mount namespace, and the retained
	// descriptor belongs to the bootstrap's. Reopen the generation here and
	// bind it only if it is the very directory the bootstrap selected.
	local, err := openRootControlled(policy.GenerationRoot, true)
	if err != nil {
		return nil, err
	}
	defer func() { _ = local.Close() }()
	if err := sameObject(generation, local); err != nil {
		return nil, err
	}
	if err := freshMountNamespace(); err != nil {
		return nil, err
	}
	// Mounts the host makes later still reach the launch, as removable and
	// network volumes must; the launch's own mounts never reach the host.
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_SLAVE, ""); err != nil {
		return nil, fmt.Errorf("make mounts receive-only: %w", err)
	}
	source := "/proc/self/fd/" + strconv.Itoa(int(local.Fd()))
	if err := unix.Mount(source, policy.LogicalInstallRoot, "", unix.MS_BIND|unix.MS_REC, ""); err != nil {
		return nil, fmt.Errorf("bind generation: %w", err)
	}
	attributes := unix.MountAttr{Attr_set: unix.MOUNT_ATTR_RDONLY | unix.MOUNT_ATTR_NOSUID | unix.MOUNT_ATTR_NODEV}
	if err := unix.MountSetattr(unix.AT_FDCWD, policy.LogicalInstallRoot, unix.AT_RECURSIVE, &attributes); err != nil {
		return nil, fmt.Errorf("seal generation mount: %w", err)
	}
	boundRoot, err := openRootControlled(policy.LogicalInstallRoot, true)
	if err != nil {
		return nil, err
	}
	err = sameObject(generation, boundRoot)
	_ = boundRoot.Close()
	if err != nil {
		return nil, fmt.Errorf("bound generation root differs from retained selection: %w", err)
	}
	var previous unix.Stat_t
	if err := unix.Fstat(int(desktop.Fd()), &previous); err != nil {
		return nil, err
	}
	mounted, err := os.Open(filepath.Join(policy.LogicalInstallRoot, "bin/painted-wolf-code"))
	if err != nil {
		return nil, err
	}
	var actual unix.Stat_t
	if err := unix.Fstat(int(mounted.Fd()), &actual); err != nil || previous.Dev != actual.Dev || previous.Ino != actual.Ino {
		_ = mounted.Close()
		return nil, errors.New("logical generation image differs from retained selection")
	}
	return mounted, nil
}

func sameObject(retained, reopened *os.File) error {
	var a, b unix.Stat_t
	if err := unix.Fstat(int(retained.Fd()), &a); err != nil {
		return err
	}
	if err := unix.Fstat(int(reopened.Fd()), &b); err != nil {
		return err
	}
	if a.Dev != b.Dev || a.Ino != b.Ino {
		return errors.New("selected generation changed before its bind")
	}
	return nil
}

// freshMountNamespace refuses to change mounts anywhere but a namespace this
// stage was cloned into: the host's mount table is never touched.
func freshMountNamespace() error {
	own, err := os.Readlink("/proc/self/ns/mnt")
	if err != nil {
		return err
	}
	host, err := os.Readlink("/proc/1/ns/mnt")
	if err != nil {
		return err
	}
	if own == host {
		return errors.New("privileged stage did not enter a private mount namespace")
	}
	return nil
}
