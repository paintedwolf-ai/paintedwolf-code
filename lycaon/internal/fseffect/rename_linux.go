//go:build linux

package fseffect

import "golang.org/x/sys/unix"

func renameNoReplace(from int, name string, to int, dest string) error {
	return unix.Renameat2(from, name, to, dest, unix.RENAME_NOREPLACE)
}
