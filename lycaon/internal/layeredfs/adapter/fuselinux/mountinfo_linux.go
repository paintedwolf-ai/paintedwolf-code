//go:build linux

package fuselinux

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

type kernelMountEntry struct {
	mountID       string
	devMajorMinor string
	mountPoint    string
	mountOptions  string
	filesystem    string
	source        string
	superOptions  string
}

func msyncMappedRegion(region []byte) error {
	if len(region) == 0 {
		return nil
	}
	return unix.Msync(region, unix.MS_SYNC)
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

func checkKernelMountTable(mountPath, expectedFSName string) (bool, bool) {
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
	if expectedFSName == "" {
		return true, true
	}
	return true, matched.source == expectedFSName
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
