//go:build darwin

package fseffect

import "golang.org/x/sys/unix"

func renameNoReplace(from int, name string, to int, dest string) error {
	return unix.RenameatxNp(from, name, to, dest, unix.RENAME_EXCL)
}
