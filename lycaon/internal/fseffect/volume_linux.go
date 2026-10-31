package fseffect

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// VolumeOf reads the volume holding file, which may be a metadata-only
// descriptor at path. Linux statfs carries no names, so the mount is the
// deepest mountinfo entry above path on the file's device.
func VolumeOf(file *os.File, path string) (Volume, error) {
	var st unix.Statfs_t
	if err := unix.Fstatfs(int(file.Fd()), &st); err != nil {
		return Volume{}, err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
		return Volume{}, err
	}
	var size uint64
	if st.Frsize > 0 {
		size = uint64(st.Frsize)
	} else if st.Bsize > 0 {
		size = uint64(st.Bsize)
	}
	vol := Volume{BlockSize: size, Blocks: st.Blocks, Free: st.Bfree, Available: st.Bavail, Files: st.Files, FreeFiles: st.Ffree, Source: "-", Target: "-"}
	mounts, err := OpenRead(Location{Root: "/proc", Rel: "self/mountinfo"})
	if err != nil {
		return vol, nil
	}
	defer func() { _ = mounts.Close() }()
	device := fmt.Sprintf("%d:%d", unix.Major(uint64(stat.Dev)), unix.Minor(uint64(stat.Dev)))
	best := -1
	scanner := bufio.NewScanner(mounts)
	for scanner.Scan() {
		if target, source, ok := mountEntry(scanner.Text(), device); ok && len(target) > best && mountHolds(target, path) {
			best, vol.Target, vol.Source = len(target), target, source
		}
	}
	return vol, nil
}

// mountEntry reads one mountinfo line's mount point and source when the
// mount is on device.
func mountEntry(line, device string) (target, source string, ok bool) {
	fields := strings.Fields(line)
	separator := -1
	for i, field := range fields {
		if field == "-" {
			separator = i
			break
		}
	}
	if len(fields) < 5 || separator < 0 || separator+2 >= len(fields) || fields[2] != device {
		return "", "", false
	}
	return unescapeMount(fields[4]), unescapeMount(fields[separator+2]), true
}

func mountHolds(target, path string) bool {
	return target == "/" || path == target || strings.HasPrefix(path, target+"/")
}

// unescapeMount decodes mountinfo's octal escapes for space, tab, newline
// and backslash.
func unescapeMount(field string) string {
	var out strings.Builder
	for i := 0; i < len(field); i++ {
		if field[i] == '\\' && i+3 < len(field) {
			if n, err := strconv.ParseUint(field[i+1:i+4], 8, 8); err == nil {
				out.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		out.WriteByte(field[i])
	}
	return out.String()
}
