//go:build unix

package launchverify

import (
	"errors"
	"os"
	"syscall"
)

// lstatFacts reads ownership without following a final symlink; maps paths
// are already resolved, so a symlink there is not the mapped file.
func lstatFacts(path string) (fileFacts, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return fileFacts{}, err
	}
	sys, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fileFacts{}, errors.New("no ownership facts")
	}
	return fileFacts{inode: uint64(sys.Ino), uid: sys.Uid, mode: info.Mode()}, nil //nolint:unconvert // Ino is narrower on some platforms.
}
