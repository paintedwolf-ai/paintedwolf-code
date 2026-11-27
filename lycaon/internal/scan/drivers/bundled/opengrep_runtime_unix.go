//go:build !windows

package bundleddriver

import (
	"fmt"
	"os"
	"syscall"
)

// checkSingleLink rejects hard-linked regular files so sealing or cleanup can
// never alter permissions on an external path sharing the same inode.
func checkSingleLink(path string, info os.FileInfo) error {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	if st.Nlink > 1 {
		return fmt.Errorf("hard-linked file rejected in runtime tree (nlink=%d): %s", st.Nlink, path)
	}
	return nil
}
