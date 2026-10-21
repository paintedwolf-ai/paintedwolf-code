//go:build linux

package launchguard

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// enterModuleView gives the calling thread, and every process it then starts,
// a mount namespace of its own in which native code maps executable only from
// the administrator's closure: the selected generation, the desktop image's
// program interpreter, the policy's system executables and its library roots.
// Every other mount, including each user-writable one, is noexec, so the
// dynamic loader cannot map a module from a user-controlled path, however a
// library names it. Propagation stays receive-only (MS_SLAVE) so volumes
// mounted after launch appear inside the desktop and engine, while the
// module-view watcher marks each newly propagated mount noexec, nosuid and
// nodev. The mounts are owned by the initial user namespace, so nothing inside
// the protected namespace can clear the attribute.
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
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_SLAVE, ""); err != nil {
		return fmt.Errorf("make module view receive-only: %w", err)
	}
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

func allowedModuleMountPoints(policy GenerationPolicy) ([]string, error) {
	desktop, err := os.Open(filepath.Join(policy.LogicalInstallRoot, "bin/painted-wolf-code"))
	if err != nil {
		return nil, err
	}
	defer func() { _ = desktop.Close() }()
	interpreter, err := programInterpreter(desktop)
	if err != nil {
		return nil, err
	}
	return moduleRoots(policy, interpreter), nil
}

func watchModuleMounts(mountinfoFD, lifetimeFD, readyFD int, installRoot string, roots []string) error {
	buf := make([]byte, 0, 64<<10)
	raw, err := readMountInfo(mountinfoFD, buf)
	if err != nil {
		return err
	}
	buf = raw
	entries := parseMountInfo(raw)
	exempt := baselineExemptMounts(entries, installRoot, roots)
	failed := map[int]string{}
	scrubModuleMounts(entries, exempt, failed)
	if readyFD >= 0 {
		if _, err := unix.Write(readyFD, []byte{1}); err != nil {
			return err
		}
		_ = unix.Close(readyFD)
	}
	fds := []unix.PollFd{
		{Fd: int32(mountinfoFD), Events: unix.POLLPRI | unix.POLLERR},
		{Fd: int32(lifetimeFD), Events: unix.POLLIN | unix.POLLHUP | unix.POLLERR},
	}
	for {
		_, err := unix.Poll(fds, -1)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return err
		}
		if fds[0].Revents&(unix.POLLPRI|unix.POLLERR) != 0 {
			raw, err := readMountInfo(mountinfoFD, buf)
			if err != nil {
				return err
			}
			buf = raw
			scrubModuleMounts(parseMountInfo(raw), exempt, failed)
		}
		if fds[1].Revents&(unix.POLLIN|unix.POLLHUP|unix.POLLERR|unix.POLLNVAL) != 0 {
			return nil
		}
	}
}

func readMountInfo(fd int, buf []byte) ([]byte, error) {
	if _, err := unix.Seek(fd, 0, io.SeekStart); err != nil {
		return buf[:0], err
	}
	buf = buf[:0]
	for {
		if len(buf) == cap(buf) {
			buf = append(buf, 0)[:len(buf)]
		}
		n, err := unix.Read(fd, buf[len(buf):cap(buf)])
		if n > 0 {
			buf = buf[:len(buf)+n]
		}
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil || n == 0 {
			return buf, err
		}
	}
}

type mountEntry struct {
	id         int
	parent     int
	mountPoint string
	options    string
}

func baselineExemptMounts(entries []mountEntry, installRoot string, roots []string) map[int]bool {
	exempt := make(map[int]bool, len(entries))
	prefix := strings.TrimRight(installRoot, "/") + "/"
	for _, entry := range entries {
		if hasMountOption(entry.options, "noexec") || entry.mountPoint == installRoot || strings.HasPrefix(entry.mountPoint, prefix) || slices.Contains(roots, entry.mountPoint) {
			exempt[entry.id] = true
		}
	}
	return exempt
}

func scrubModuleMounts(entries []mountEntry, exempt map[int]bool, failed map[int]string) {
	attr := unix.MountAttr{Attr_set: unix.MOUNT_ATTR_NOEXEC | unix.MOUNT_ATTR_NOSUID | unix.MOUNT_ATTR_NODEV}
	for _, entry := range entries {
		if exempt[entry.id] {
			continue
		}
		if hasNoexecNosuidNodev(entry.options) {
			delete(failed, entry.id)
			continue
		}
		if isShadowedMount(entry, entries) || failed[entry.id] == entry.options {
			continue
		}
		if err := unix.MountSetattr(unix.AT_FDCWD, entry.mountPoint, unix.AT_SYMLINK_NOFOLLOW, &attr); err != nil {
			failed[entry.id] = entry.options
		}
	}
}

func isShadowedMount(target mountEntry, entries []mountEntry) bool {
	for _, other := range entries {
		if other.id != target.id && other.parent == target.id && other.mountPoint == target.mountPoint {
			return true
		}
	}
	return false
}

func hasNoexecNosuidNodev(options string) bool {
	return hasMountOption(options, "noexec") && hasMountOption(options, "nosuid") && hasMountOption(options, "nodev")
}

func hasMountOption(options, want string) bool {
	for len(options) > 0 {
		token, rest, found := strings.Cut(options, ",")
		if token == want {
			return true
		}
		if !found {
			break
		}
		options = rest
	}
	return false
}

func parseMountInfo(raw []byte) []mountEntry {
	var entries []mountEntry
	for line := range strings.SplitSeq(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 6 {
			continue
		}
		id, err1 := strconv.Atoi(fields[0])
		parent, err2 := strconv.Atoi(fields[1])
		if err1 != nil || err2 != nil {
			continue
		}
		entries = append(entries, mountEntry{
			id:         id,
			parent:     parent,
			mountPoint: unescapeMountPath(fields[4]),
			options:    fields[5],
		})
	}
	return entries
}

func unescapeMountPath(raw string) string {
	if !strings.ContainsRune(raw, '\\') {
		return raw
	}
	var out strings.Builder
	out.Grow(len(raw))
	for i := 0; i < len(raw); i++ {
		if raw[i] == '\\' && i+3 < len(raw) && isOctal(raw[i+1]) && isOctal(raw[i+2]) && isOctal(raw[i+3]) {
			out.WriteByte(((raw[i+1] - '0') << 6) | ((raw[i+2] - '0') << 3) | (raw[i+3] - '0'))
			i += 3
			continue
		}
		out.WriteByte(raw[i])
	}
	return out.String()
}

func isOctal(b byte) bool {
	return b >= '0' && b <= '7'
}
