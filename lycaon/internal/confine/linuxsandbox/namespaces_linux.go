//go:build linux

package linuxsandbox

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/sys/unix"
)

// Inside reports whether this process already runs in the namespaces the plan
// expects: its user namespace differs from the host's.
func Inside(plan Plan) bool {
	host := plan.Attest.HostNamespaces[NamespaceUser]
	own, err := os.Readlink("/proc/self/ns/" + NamespaceUser)
	return err == nil && host != "" && own != host
}

// mountProc gives the new PID namespace its own process table first, so any
// later reader of /proc, including a failure path, sees sandbox processes only.
func mountProc() error {
	if err := unix.Mount("proc", "/proc", "proc", unix.MS_NOSUID|unix.MS_NODEV|unix.MS_NOEXEC, ""); err != nil {
		return &Unavailability{Code: UnavailableMount, Detail: "mount /proc: " + err.Error()}
	}
	return nil
}

// makeRootPrivate keeps the sandbox's mounts from propagating anywhere.
func makeRootPrivate() error {
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		return &Unavailability{Code: UnavailableMount, Detail: "make / private: " + err.Error()}
	}
	return nil
}

// mountOp is one mount the filesystem plan asks for, applied in path order so
// a parent's mask exists before anything beneath it.
type mountOp struct {
	path string
	kind opKind
	// clone is a detached copy of the real path, taken before any mask could
	// hide it; only restored paths carry one.
	clone int
}

type opKind int

const (
	opHidden opKind = iota
	opReadOnly
	opRestored
)

// applyFilesystem expresses the plan's masks and restorations as mounts.
func applyFilesystem(fs Filesystem) error {
	var ops []mountOp
	for _, p := range fs.Hidden {
		if exists(p) {
			ops = append(ops, mountOp{path: p, kind: opHidden})
		}
	}
	for _, p := range fs.ReadOnly {
		if exists(p) {
			ops = append(ops, mountOp{path: p, kind: opReadOnly})
		}
	}
	// Restored paths are cloned now, while their real inodes are still visible.
	for _, p := range fs.Restored {
		if !exists(p) {
			continue
		}
		fd, err := cloneTree(p)
		if err != nil {
			return mountFailure("clone "+p, err)
		}
		ops = append(ops, mountOp{path: p, kind: opRestored, clone: fd})
	}
	sort.SliceStable(ops, func(i, j int) bool { return ops[i].path < ops[j].path })
	hiddenFile := -1
	defer func() {
		if hiddenFile >= 0 {
			_ = unix.Close(hiddenFile)
		}
	}()
	for _, op := range ops {
		var err error
		switch op.kind {
		case opHidden:
			if isDir(op.path) {
				err = hideDirectory(op.path)
			} else {
				if hiddenFile < 0 {
					hiddenFile, err = newTmpfs("0755")
				}
				if err == nil {
					err = hideFile(hiddenFile, op.path)
				}
			}
		case opReadOnly:
			err = bindReadOnly(op.path)
		case opRestored:
			err = restore(op.clone, op.path)
			_ = unix.Close(op.clone)
		}
		if err != nil {
			return mountFailure(op.path, err)
		}
	}
	// Hidden directories become read-only only after anything restored beneath
	// them has its mountpoint.
	for _, op := range ops {
		if op.kind == opHidden && isDir(op.path) {
			if err := setReadOnlyAt(op.path); err != nil {
				return mountFailure(op.path, err)
			}
		}
	}
	return nil
}

func mountFailure(what string, err error) error {
	return &Unavailability{Code: UnavailableMount, Detail: what + ": " + err.Error()}
}

// hideDirectory replaces a directory with an empty tmpfs. The placeholder is
// writable only until anything restored beneath it has its mountpoint; the
// final read-only pass seals it.
func hideDirectory(path string) error {
	fd, err := newTmpfs("0755")
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(fd) }()
	return unix.MoveMount(fd, "", unix.AT_FDCWD, path, unix.MOVE_MOUNT_F_EMPTY_PATH)
}

// hideFile replaces a file with an empty, unreadable file from the shared tmpfs.
func hideFile(tmpfs int, path string) error {
	name := "hidden"
	fd, err := unix.Openat(tmpfs, name, unix.O_CREAT|unix.O_WRONLY|unix.O_CLOEXEC, 0)
	if err != nil && !errors.Is(err, unix.EEXIST) {
		return err
	}
	if err == nil {
		_ = unix.Close(fd)
	}
	clone, err := unix.OpenTree(tmpfs, name, unix.OPEN_TREE_CLONE|unix.O_CLOEXEC)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(clone) }()
	if err := unix.MountSetattr(clone, "", unix.AT_EMPTY_PATH, &unix.MountAttr{Attr_set: unix.MOUNT_ATTR_RDONLY}); err != nil {
		return err
	}
	return unix.MoveMount(clone, "", unix.AT_FDCWD, path, unix.MOVE_MOUNT_F_EMPTY_PATH)
}

// bindReadOnly covers a path with a read-only copy of itself.
func bindReadOnly(path string) error {
	clone, err := cloneTree(path)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(clone) }()
	attr := unix.MountAttr{Attr_set: unix.MOUNT_ATTR_RDONLY}
	if err := unix.MountSetattr(clone, "", unix.AT_EMPTY_PATH|unix.AT_RECURSIVE, &attr); err != nil {
		return err
	}
	return unix.MoveMount(clone, "", unix.AT_FDCWD, path, unix.MOVE_MOUNT_F_EMPTY_PATH)
}

// restore attaches a clone of the real path over whatever now covers it,
// creating the mountpoint inside a hidden placeholder when needed.
func restore(clone int, path string) error {
	var st unix.Stat_t
	if err := unix.Fstat(clone, &st); err != nil {
		return err
	}
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		if st.Mode&unix.S_IFMT == unix.S_IFDIR {
			if err := os.MkdirAll(path, 0o755); err != nil {
				return err
			}
		} else {
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			if err := createPlaceholder(path, 0o444); err != nil {
				return err
			}
		}
	}
	return unix.MoveMount(clone, "", unix.AT_FDCWD, path, unix.MOVE_MOUNT_F_EMPTY_PATH)
}

// createPlaceholder makes an empty file inside a sandbox tmpfs to serve as a
// mountpoint; it is never a durable write.
func createPlaceholder(path string, mode uint32) error {
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_WRONLY|unix.O_CLOEXEC, mode)
	if err != nil {
		return err
	}
	return unix.Close(fd)
}

// cloneTree detaches a copy of the mount tree at path.
func cloneTree(path string) (int, error) {
	return unix.OpenTree(unix.AT_FDCWD, path, unix.OPEN_TREE_CLONE|unix.AT_RECURSIVE|unix.O_CLOEXEC)
}

// newTmpfs creates an unattached tmpfs with the given root mode.
func newTmpfs(mode string) (int, error) {
	fs, err := unix.Fsopen("tmpfs", unix.FSOPEN_CLOEXEC)
	if err != nil {
		return -1, err
	}
	defer func() { _ = unix.Close(fs) }()
	for key, value := range map[string]string{"mode": mode, "size": "1m"} {
		if err := unix.FsconfigSetString(fs, key, value); err != nil {
			return -1, err
		}
	}
	if err := unix.FsconfigCreate(fs); err != nil {
		return -1, err
	}
	return unix.Fsmount(fs, unix.FSMOUNT_CLOEXEC, unix.MOUNT_ATTR_NOSUID|unix.MOUNT_ATTR_NODEV)
}

// setReadOnlyAt makes the top mount at path read-only without touching mounts
// beneath it.
func setReadOnlyAt(path string) error {
	return unix.MountSetattr(unix.AT_FDCWD, path, 0, &unix.MountAttr{Attr_set: unix.MOUNT_ATTR_RDONLY})
}

// devNodes are the host device nodes a confined process may use.
var devNodes = []string{"null", "zero", "full", "random", "urandom", "tty"}

// mountDev replaces /dev with a curated set: the standard character devices,
// the host's pseudo-terminals so inherited terminals keep their names, a fresh
// shared-memory tmpfs, and the descriptor links. Raw disks and other devices
// are absent rather than denied.
func mountDev() error {
	clones := map[string]int{}
	defer func() {
		for _, fd := range clones {
			_ = unix.Close(fd)
		}
	}()
	for _, name := range devNodes {
		fd, err := unix.OpenTree(unix.AT_FDCWD, "/dev/"+name, unix.OPEN_TREE_CLONE|unix.O_CLOEXEC)
		if err != nil {
			return mountFailure("/dev/"+name, err)
		}
		clones[name] = fd
	}
	if fd, err := cloneTree("/dev/pts"); err == nil {
		clones["pts"] = fd
	}
	ptmxLink, _ := os.Readlink("/dev/ptmx")
	if ptmxLink == "" {
		if fd, err := unix.OpenTree(unix.AT_FDCWD, "/dev/ptmx", unix.OPEN_TREE_CLONE|unix.O_CLOEXEC); err == nil {
			clones["ptmx"] = fd
		}
	}
	dev, err := newTmpfs("0755")
	if err != nil {
		return mountFailure("/dev", err)
	}
	defer func() { _ = unix.Close(dev) }()
	if err := unix.MoveMount(dev, "", unix.AT_FDCWD, "/dev", unix.MOVE_MOUNT_F_EMPTY_PATH); err != nil {
		return mountFailure("/dev", err)
	}
	for name, fd := range clones {
		target := "/dev/" + name
		if name == "pts" {
			if err := os.Mkdir(target, 0o755); err != nil {
				return mountFailure(target, err)
			}
		} else {
			if err := createPlaceholder(target, 0o666); err != nil {
				return mountFailure(target, err)
			}
		}
		if err := unix.MoveMount(fd, "", unix.AT_FDCWD, target, unix.MOVE_MOUNT_F_EMPTY_PATH); err != nil {
			return mountFailure(target, err)
		}
	}
	if ptmxLink != "" {
		if err := os.Symlink(ptmxLink, "/dev/ptmx"); err != nil {
			return mountFailure("/dev/ptmx", err)
		}
	}
	for link, target := range map[string]string{"fd": "/proc/self/fd", "stdin": "/proc/self/fd/0", "stdout": "/proc/self/fd/1", "stderr": "/proc/self/fd/2"} {
		if err := os.Symlink(target, "/dev/"+link); err != nil {
			return mountFailure("/dev/"+link, err)
		}
	}
	if err := os.Mkdir("/dev/shm", 0o1777); err != nil {
		return mountFailure("/dev/shm", err)
	}
	shm, err := newTmpfs("1777")
	if err != nil {
		return mountFailure("/dev/shm", err)
	}
	defer func() { _ = unix.Close(shm) }()
	if err := unix.MoveMount(shm, "", unix.AT_FDCWD, "/dev/shm", unix.MOVE_MOUNT_F_EMPTY_PATH); err != nil {
		return mountFailure("/dev/shm", err)
	}
	if err := setReadOnlyAt("/dev"); err != nil {
		return mountFailure("/dev", err)
	}
	return nil
}

// loopbackUp brings the namespace's loopback interface up; nothing else exists.
func loopbackUp() error {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("loopback socket: %w", err)
	}
	defer func() { _ = unix.Close(fd) }()
	ifr, err := unix.NewIfreq("lo")
	if err != nil {
		return err
	}
	if err := unix.IoctlIfreq(fd, unix.SIOCGIFFLAGS, ifr); err != nil {
		return fmt.Errorf("read lo flags: %w", err)
	}
	ifr.SetUint16(ifr.Uint16() | unix.IFF_UP | unix.IFF_RUNNING)
	if err := unix.IoctlIfreq(fd, unix.SIOCSIFFLAGS, ifr); err != nil {
		return fmt.Errorf("bring lo up: %w", err)
	}
	return nil
}

// namespacesDiffer confirms every namespace kind differs from the host's.
func namespacesDiffer(host map[string]string) error {
	var same []string
	for _, kind := range NamespaceKinds() {
		own, err := os.Readlink("/proc/self/ns/" + kind)
		if err != nil {
			return &Unavailability{Code: UnavailableNamespaces, Detail: kind + ": " + err.Error()}
		}
		if want, ok := host[kind]; !ok || own == want {
			same = append(same, kind)
		}
	}
	if len(same) > 0 {
		return &Unavailability{Code: UnavailableNamespaces, Detail: "shared with the host: " + strings.Join(same, ", ")}
	}
	return nil
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func isDir(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
}
