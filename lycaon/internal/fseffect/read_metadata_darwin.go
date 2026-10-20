//go:build darwin

package fseffect

import "golang.org/x/sys/unix"

func metadataOpenFlags(directory bool) int {
	if directory {
		return unix.O_SEARCH
	}
	return unix.O_EXEC
}
