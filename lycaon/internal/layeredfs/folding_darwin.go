//go:build darwin

package layeredfs

import (
	"strings"

	"golang.org/x/sys/unix"
)

const pathconfCaseSensitive = 11 // _PC_CASE_SENSITIVE

// ProbeNameFolding reads how the filesystem holding dir compares names. APFS and HFS+ compare
// composed and decomposed names as one; whether case matters is the volume's own answer.
func ProbeNameFolding(dir string) NameFolding {
	var folding NameFolding
	if sensitive, err := unix.Pathconf(dir, pathconfCaseSensitive); err == nil {
		folding.Case = sensitive == 0
	}
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err == nil {
		switch strings.TrimRight(string(st.Fstypename[:]), "\x00") {
		case "apfs", "hfs":
			folding.Unicode = true
		}
	}
	return folding
}
