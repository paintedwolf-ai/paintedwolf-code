//go:build linux

package lineage

import (
	"bufio"
	"bytes"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// hostProc is the procfs mount the kernel tables are read from.
var hostProc = procFS{root: "/proc"}

// pipePeerHandle returns the pipe's inode. Both ends share it, so holders are
// narrowed to write ends by their open flags.
func pipePeerHandle(read *os.File) (uint64, error) {
	conn, err := read.SyscallConn()
	if err != nil {
		return 0, err
	}
	var st syscall.Stat_t
	var statErr error
	// Fd would switch the descriptor to blocking, which pipeHasWriters cannot afford.
	if err := conn.Control(func(fd uintptr) { statErr = syscall.Fstat(int(fd), &st) }); err != nil {
		return 0, err
	}
	if statErr != nil {
		return 0, statErr
	}
	return st.Ino, nil
}

// pipeHasWriters probes the read end without consuming the poller.
func pipeHasWriters(read *os.File) bool {
	conn, err := read.SyscallConn()
	if err != nil {
		return false
	}
	writers := false
	var buf [1]byte
	_ = conn.Control(func(fd uintptr) {
		n, err := syscall.Read(int(fd), buf[:])
		// End of file means every write end closed; anything else means one is open.
		writers = !(err == nil && n == 0)
	})
	return writers
}

// pipeHolders answers for every lineage in one walk of the process table.
func pipeHolders(handles []uint64) ([][]int, error) {
	out := make([][]int, len(handles))
	if len(handles) == 0 {
		return out, nil
	}
	index := make(map[uint64]int, len(handles))
	for i, h := range handles {
		index[h] = i
	}
	pids, err := hostProc.livePIDs()
	if err != nil {
		return nil, err
	}
	self := os.Getpid()
	for _, pid := range pids {
		if pid == self {
			continue
		}
		held := map[int]bool{}
		hostProc.forEachFD(pid, func(fd, target string) {
			inode, ok := linkInode(target, "pipe:[")
			if !ok {
				return
			}
			i, wanted := index[inode]
			if !wanted || held[i] || !fdWritable(pid, fd) {
				return
			}
			held[i] = true
			out[i] = append(out[i], pid)
		})
	}
	return out, nil
}

// peerPID finds the process holding the client end of a loopback connection.
func peerPID(local, remote netip.AddrPort) (int, bool) {
	sockets, err := hostProc.sockets([]Protocol{ProtocolTCP})
	if err != nil {
		return 0, false
	}
	// The peer's local endpoint is the connection's remote one.
	var inode uint64
	for _, s := range sockets {
		if sameEndpoint(s.Local, remote) && sameEndpoint(s.Remote, local) {
			inode = s.Inode
			break
		}
	}
	if inode == 0 {
		return 0, false
	}
	holders, err := hostProc.socketHolders(map[uint64]bool{inode: true})
	if err != nil || len(holders[inode]) == 0 {
		return 0, false
	}
	return holders[inode][0], true
}

func listeners(query ListenerQuery) ([]Listener, error) { return hostProc.listeners(query) }

func livePIDs() ([]int, error) { return hostProc.livePIDs() }

// fdWritable reports whether a descriptor was opened for writing.
func fdWritable(pid int, fd string) bool {
	data, err := os.ReadFile(filepath.Join(hostProc.root, strconv.Itoa(pid), "fdinfo", fd))
	if err != nil {
		return false
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		value, ok := strings.CutPrefix(scanner.Text(), "flags:")
		if !ok {
			continue
		}
		flags, err := strconv.ParseUint(strings.TrimSpace(value), 8, 64)
		return err == nil && flags&(syscall.O_WRONLY|syscall.O_RDWR) != 0
	}
	return false
}

const supported = true
