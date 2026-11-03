//go:build linux

package fuselinux

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const deadMountProbeBound = 5 * time.Second

type kernelMountEntry struct {
	mountID       string
	devMajorMinor string
	mountPoint    string
	mountOptions  string
	filesystem    string
	source        string
	superOptions  string
}

func statDeviceID(path string) (uint64, error) {
	var st unix.Stat_t
	if err := unix.Stat(path, &st); err != nil {
		return 0, err
	}
	return uint64(st.Dev), nil
}

func checkFUSEDeviceAvailable(path string) error {
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	_ = unix.Close(fd)
	return nil
}

func closeRawFD(fd int) {
	if fd >= 0 {
		_ = unix.Close(fd)
	}
}

func verifyKernelMountInfo(mountPath, expectedFSName string, uid, gid uint32) (kernelMountEntry, error) {
	canonicalMount := canonicalPath(mountPath)
	entries, err := readMountInfoEntries()
	if err != nil {
		return kernelMountEntry{}, err
	}
	var matched *kernelMountEntry
	for i := range entries {
		entry := &entries[i]
		if strings.HasPrefix(entry.filesystem, "fuse") && filepath.Clean(entry.mountPoint) == canonicalMount {
			matched = entry
		}
	}
	if matched == nil {
		return kernelMountEntry{}, fmt.Errorf("fuselinux: kernel mount table has no active FUSE entry for %s", canonicalMount)
	}
	wantFSType := "fuse." + DefaultSubtype
	if matched.filesystem != wantFSType {
		return kernelMountEntry{}, fmt.Errorf("fuselinux: unexpected kernel fstype %q (want %s)", matched.filesystem, wantFSType)
	}
	if expectedFSName != "" && matched.source != "" && matched.source != expectedFSName {
		return kernelMountEntry{}, fmt.Errorf("fuselinux: unexpected mount source %q (want %q)", matched.source, expectedFSName)
	}
	for _, want := range []string{"nosuid", "nodev"} {
		if !hasMountOption(matched.mountOptions, want) {
			return kernelMountEntry{}, fmt.Errorf("fuselinux: mount options %q missing %s", matched.mountOptions, want)
		}
	}
	wantUID := fmt.Sprintf("user_id=%d", uid)
	wantGID := fmt.Sprintf("group_id=%d", gid)
	hasUID := false
	hasGID := false
	for _, opt := range strings.Split(matched.superOptions, ",") {
		if opt == wantUID {
			hasUID = true
		}
		if opt == wantGID {
			hasGID = true
		}
		if opt == "allow_other" || strings.HasPrefix(opt, "allow_other=") {
			return kernelMountEntry{}, fmt.Errorf("fuselinux: forbidden allow_other present in super options %q", matched.superOptions)
		}
	}
	for _, opt := range strings.Split(matched.mountOptions, ",") {
		if opt == "allow_other" || strings.HasPrefix(opt, "allow_other=") {
			return kernelMountEntry{}, fmt.Errorf("fuselinux: forbidden allow_other present in mount options %q", matched.mountOptions)
		}
	}
	if !hasUID || !hasGID {
		return kernelMountEntry{}, fmt.Errorf("fuselinux: super options %q missing %s or %s", matched.superOptions, wantUID, wantGID)
	}
	return *matched, nil
}

func hasMountOption(options, want string) bool {
	for _, opt := range strings.Split(options, ",") {
		if opt == want {
			return true
		}
	}
	return false
}

// checkKernelMountTable matches the mount at mountPath against its recorded source and, once
// known, the connection's device number, so another mount at the same path is never adopted.
func checkKernelMountTable(mountPath, expectedFSName, expectedDev string) (bool, bool) {
	canonicalMount := canonicalPath(mountPath)
	entries, err := readMountInfoEntries()
	if err != nil {
		return false, false
	}
	var matched *kernelMountEntry
	for i := range entries {
		entry := &entries[i]
		if strings.HasPrefix(entry.filesystem, "fuse") && filepath.Clean(entry.mountPoint) == canonicalMount {
			matched = entry
		}
	}
	if matched == nil {
		return false, false
	}
	if expectedFSName != "" && matched.source != expectedFSName {
		return true, false
	}
	if expectedDev != "" && matched.devMajorMinor != expectedDev {
		return true, false
	}
	return true, true
}

// deadViewMountPaths returns this user's view mounts under runtimeRoot whose FUSE connection has
// ended. statfs always reaches the connection, and ENOTCONN is the kernel's report that it is gone. A mount that does not answer within the probe
// bound still has a server, however slow, and is left alone.
func deadViewMountPaths(ctx context.Context, runtimeRoot string, uid uint32) ([]string, error) {
	entries, err := readMountInfoEntries()
	if err != nil {
		return nil, err
	}
	root := canonicalPath(runtimeRoot)
	wantOwner := fmt.Sprintf("user_id=%d", uid)
	var dead []string
	for _, entry := range entries {
		if entry.filesystem != "fuse."+DefaultSubtype || !hasMountOption(entry.superOptions, wantOwner) || !pathWithin(root, entry.mountPoint) {
			continue
		}
		if connectionEnded(ctx, entry.mountPoint) {
			dead = append(dead, entry.mountPoint)
		}
	}
	return dead, nil
}

func pathWithin(root, candidate string) bool {
	rel, err := filepath.Rel(root, filepath.Clean(candidate))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, "../")
}

// mountPathsWithin lists every mount at or below root, of any filesystem type.
func mountPathsWithin(root string) ([]string, error) {
	entries, err := readMountInfoEntries()
	if err != nil {
		return nil, err
	}
	canonicalRoot := canonicalPath(root)
	var within []string
	for _, entry := range entries {
		if pathWithin(canonicalRoot, entry.mountPoint) {
			within = append(within, entry.mountPoint)
		}
	}
	return within, nil
}

func connectionEnded(ctx context.Context, mountPoint string) bool {
	probeCtx, cancel := context.WithTimeout(ctx, deadMountProbeBound)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		var st unix.Statfs_t
		result <- unix.Statfs(mountPoint, &st)
	}()
	select {
	case err := <-result:
		return errors.Is(err, unix.ENOTCONN)
	case <-probeCtx.Done():
		return false
	}
}

// syncMountedFilesystem writes back every dirty page of the mount holding path. It serves files
// a descriptor cannot be opened on; per-inode errors are reported only through fsync.
func syncMountedFilesystem(path string) error {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = unix.Close(fd) }()
	return unix.Syncfs(fd)
}

func readMountInfoEntries() ([]kernelMountEntry, error) {
	data, err := os.ReadFile("/proc/thread-self/mountinfo")
	if err != nil {
		data, err = os.ReadFile("/proc/self/mountinfo")
		if err != nil {
			return nil, fmt.Errorf("fuselinux: read mountinfo: %w", err)
		}
	}
	var out []kernelMountEntry
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if entry, ok := parseMountInfoLine(line); ok {
			out = append(out, entry)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func parseMountInfoLine(line string) (kernelMountEntry, bool) {
	sepIdx := strings.Index(line, " - ")
	if sepIdx < 0 {
		return kernelMountEntry{}, false
	}
	left := strings.Fields(line[:sepIdx])
	right := strings.Fields(line[sepIdx+3:])
	if len(left) < 6 || len(right) < 3 {
		return kernelMountEntry{}, false
	}
	return kernelMountEntry{
		mountID:       left[0],
		devMajorMinor: left[2],
		mountPoint:    unescapeMountInfoField(left[4]),
		mountOptions:  left[5],
		filesystem:    right[0],
		source:        unescapeMountInfoField(right[1]),
		superOptions:  right[2],
	}, true
}

func unescapeMountInfoField(raw string) string {
	var b strings.Builder
	for i := 0; i < len(raw); i++ {
		if raw[i] == '\\' && i+3 < len(raw) {
			if v, err := strconv.ParseUint(raw[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(v))
				i += 3
				continue
			}
		}
		b.WriteByte(raw[i])
	}
	return b.String()
}

func canonicalPath(path string) string {
	clean := filepath.Clean(path)
	if resolved, err := filepath.EvalSymlinks(clean); err == nil {
		return filepath.Clean(resolved)
	}
	parent := filepath.Dir(clean)
	if resolvedParent, parentErr := filepath.EvalSymlinks(parent); parentErr == nil {
		return filepath.Join(resolvedParent, filepath.Base(clean))
	}
	return clean
}
