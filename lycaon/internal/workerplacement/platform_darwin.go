//go:build darwin

package workerplacement

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func volumeOf(path string) (Volume, error) {
	var stat unix.Stat_t
	if err := unix.Stat(path, &stat); err != nil {
		return Volume{}, err
	}
	var mount unix.Statfs_t
	if err := unix.Statfs(path, &mount); err != nil {
		return Volume{}, err
	}
	root := unix.ByteSliceToString(mount.Mntonname[:])
	if root == "" {
		return Volume{}, fmt.Errorf("volume of %s has no mount point", path)
	}
	return Volume{ID: uint64(stat.Dev), Root: root}, nil
}

func readOnlyFilesystem(err error) bool {
	return errors.Is(err, unix.EROFS)
}

// /var/tmp survives a restart; the per-user temporary folder may not.
func fallbackParents() []string {
	return []string{"/private/var/tmp", os.TempDir()}
}

// Binary property list of the string "com.apple.backupd", the value Time Machine
// stores for an excluded item.
var backupExcludeValue = append(append([]byte("bplist00_\x10\x11"), "com.apple.backupd"...),
	0x08, 0, 0, 0, 0, 0, 0, 1, 1, 0, 0, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 28)

const backupExcludeAttribute = "com.apple.metadata:com_apple_backup_excludeItem"

// The .noindex name and this file keep Spotlight out; the attribute keeps Time
// Machine out. Backup tools that ignore the attribute are outside this adapter.
func excludeFromBackupsAndIndexing(folder string) error {
	return errors.Join(
		unix.Setxattr(folder, backupExcludeAttribute, backupExcludeValue, 0),
		writeFolderFile(filepath.Join(folder, ".metadata_never_index"), nil),
	)
}
