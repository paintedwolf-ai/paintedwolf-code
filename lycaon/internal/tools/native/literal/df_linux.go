package literal

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// statVolume reads the volume holding an open file. Linux statfs carries no
// names, so the mount comes from /proc/self/mountinfo: the deepest mount
// point above path on the file's device, as GNU df chooses it.
func statVolume(file *os.File, path string) (volume, error) {
	var st unix.Statfs_t
	if err := unix.Fstatfs(int(file.Fd()), &st); err != nil {
		return volume{}, err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
		return volume{}, err
	}
	size := uint64(st.Frsize)
	if size == 0 {
		size = uint64(st.Bsize)
	}
	vol := volume{blockSize: size, blocks: st.Blocks, free: st.Bfree, avail: st.Bavail, files: st.Files, freeFiles: st.Ffree, source: "-", target: "-"}
	mounts, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return vol, nil
	}
	defer func() { _ = mounts.Close() }()
	device := fmt.Sprintf("%d:%d", unix.Major(uint64(stat.Dev)), unix.Minor(uint64(stat.Dev)))
	best := -1
	scanner := bufio.NewScanner(mounts)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		separator := -1
		for i, field := range fields {
			if field == "-" {
				separator = i
				break
			}
		}
		if len(fields) < 5 || separator < 0 || separator+2 >= len(fields) || fields[2] != device {
			continue
		}
		target := unescapeMount(fields[4])
		if len(target) > best && (target == "/" || path == target || strings.HasPrefix(path, target+"/")) {
			best, vol.target, vol.source = len(target), target, unescapeMount(fields[separator+2])
		}
	}
	return vol, nil
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
