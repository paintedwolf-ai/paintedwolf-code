//go:build linux

package workerplacement

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

func volumeOf(path string) (Volume, error) {
	var stat unix.Stat_t
	if err := unix.Stat(path, &stat); err != nil {
		return Volume{}, err
	}
	raw, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return Volume{}, err
	}
	root := mountPointOf(string(raw), path)
	if root == "" {
		return Volume{}, fmt.Errorf("no mount point for %s", path)
	}
	return Volume{ID: stat.Dev, Root: root}, nil
}

// mountPointOf returns the longest mount point containing path. A later entry
// for the same point mounted over an earlier one.
func mountPointOf(mountinfo, path string) string {
	best := ""
	for _, line := range strings.Split(mountinfo, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		point := unescapeMountPoint(fields[4])
		if atOrUnder(path, point) && len(point) >= len(best) {
			best = point
		}
	}
	return best
}

func unescapeMountPoint(field string) string {
	return strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`).Replace(field)
}

func readOnlyFilesystem(err error) bool {
	return errors.Is(err, unix.EROFS)
}

func fallbackParents() []string {
	return []string{"/var/tmp", os.TempDir()}
}

const cacheDirTag = "Signature: 8a477f597d28d172789f06886806bc55\n# Painted Wolf Code worker branches. Rebuilt on demand.\n"

// Linux has no system-wide exclusion. CACHEDIR.TAG reaches backup tools that honor
// the convention; .trackerignore reaches GNOME search. The dot name keeps the folder
// out of most desktop indexers by their default settings.
func excludeFromBackupsAndIndexing(folder string) error {
	return errors.Join(
		writeFolderFile(filepath.Join(folder, "CACHEDIR.TAG"), []byte(cacheDirTag)),
		writeFolderFile(filepath.Join(folder, ".trackerignore"), nil),
	)
}
