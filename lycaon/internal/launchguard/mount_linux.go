//go:build linux

package launchguard

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// prepareEntry runs only in the root-created private namespace, before login
// credentials or application code. The sealed policy fixes both mount roots.
func prepareEntry(spec EntrySpec, origin *os.File) error {
	if os.Getuid() != 0 || os.Geteuid() != 0 || os.Getgid() != 0 || os.Getegid() != 0 {
		return errors.New("entry lacks its fixed namespace setup credentials")
	}
	if err := verifyNamespace(origin); err != nil {
		return err
	}
	generation := os.NewFile(entryGenerationFD, "selected-generation-root")
	defer func() { _ = generation.Close() }()
	info, err := generation.Stat()
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("selected generation is not a directory")
	}
	if err := trustedObject(info); err != nil {
		return err
	}
	target, err := openRootControlled(spec.Generation.LogicalInstallRoot, true)
	if err != nil {
		return err
	}
	_ = target.Close()
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		return err
	}
	if err := unix.Mount("/proc/self/fd/11", spec.Generation.LogicalInstallRoot, "", unix.MS_BIND|unix.MS_REC, ""); err != nil {
		return err
	}
	attributes := unix.MountAttr{Attr_set: unix.MOUNT_ATTR_RDONLY | unix.MOUNT_ATTR_NOSUID | unix.MOUNT_ATTR_NODEV}
	if err := unix.MountSetattr(unix.AT_FDCWD, spec.Generation.LogicalInstallRoot, unix.AT_RECURSIVE, &attributes); err != nil {
		return err
	}
	// Reopen through the mounted logical name so /proc/exe and file mappings
	// resolve in the process's own filesystem view, while retaining inode proof.
	logical := filepath.Join(spec.Generation.LogicalInstallRoot, "bin/painted-wolf-code")
	var previous unix.Stat_t
	if err := unix.Fstat(entryImageFD, &previous); err != nil {
		return err
	}
	mounted, err := os.Open(logical)
	if err != nil {
		return err
	}
	defer func() { _ = mounted.Close() }()
	var actual unix.Stat_t
	if err := unix.Fstat(int(mounted.Fd()), &actual); err != nil {
		return err
	}
	if previous.Dev != actual.Dev || previous.Ino != actual.Ino {
		return errors.New("logical generation image differs from retained selection")
	}
	if err := unix.Dup3(int(mounted.Fd()), entryImageFD, unix.O_CLOEXEC); err != nil {
		return err
	}
	groups := make([]int, len(spec.Groups))
	for i, group := range spec.Groups {
		groups[i] = int(group)
	}
	// x/sys uses Go's all-thread credential syscalls, removing setup identity
	// from the runtime's existing threads before any application exec.
	if err := unix.Setgroups(groups); err != nil {
		return err
	}
	if err := unix.Setresgid(int(spec.GID), int(spec.GID), int(spec.GID)); err != nil {
		return err
	}
	if err := unix.Setresuid(int(spec.UID), int(spec.UID), int(spec.UID)); err != nil {
		return err
	}
	return SealEntry(origin)
}
