//go:build linux

package lineage

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
)

// SocketsOf joins native TCP and descriptor tables for one current incarnation.
// Inaccessible descriptor tables are failures, never empty ownership evidence.
func SocketsOf(pid int) ([]SocketObservation, error) {
	holder, ok := processIdentity(pid)
	if !ok {
		return nil, syscall.ESRCH
	}
	sockets, err := procSockets()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(filepath.Join(procRoot, strconv.Itoa(pid), "fd"))
	if err != nil {
		return nil, err
	}
	inodes := map[uint64]bool{}
	for _, entry := range entries {
		target, err := os.Readlink(filepath.Join(procRoot, strconv.Itoa(pid), "fd", entry.Name()))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if inode, ok := linkInode(target, "socket:["); ok {
			inodes[inode] = true
		}
	}
	if !holder.Current() {
		return nil, syscall.ESRCH
	}
	var result []SocketObservation
	for _, socket := range sockets {
		if inodes[socket.Inode] {
			result = append(result, SocketObservation{Local: socket.Local, Remote: socket.Remote,
				ResourceID: fmt.Sprintf("linux-tcp:%d", socket.Inode), Listening: socket.Listening, Holder: holder})
		}
	}
	return result, nil
}
