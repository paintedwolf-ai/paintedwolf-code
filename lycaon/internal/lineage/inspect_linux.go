//go:build linux

package lineage

import (
	"bufio"
	"bytes"
	"errors"
	"io/fs"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// procRoot is the procfs mount the kernel tables are read from.
const procRoot = "/proc"

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
	pids, err := livePIDs()
	if err != nil {
		return nil, err
	}
	self := os.Getpid()
	for _, pid := range pids {
		if pid == self {
			continue
		}
		held := map[int]bool{}
		forEachFD(pid, func(fd, target string) {
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
	sockets, err := procSockets()
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
	holders, err := socketHolders(map[uint64]bool{inode: true})
	if err != nil || len(holders[inode]) == 0 {
		return 0, false
	}
	return holders[inode][0], true
}

func listeners() ([]Listener, error) {
	sockets, err := procSockets()
	if err != nil {
		return nil, err
	}
	wanted := map[uint64]bool{}
	var listening []procSocket
	for _, s := range sockets {
		if s.Listening {
			listening = append(listening, s)
			wanted[s.Inode] = true
		}
	}
	holders, err := socketHolders(wanted)
	if err != nil {
		return nil, err
	}
	out := make([]Listener, 0, len(listening))
	for _, s := range listening {
		out = append(out, Listener{Addr: s.Local, PIDs: holders[s.Inode]})
	}
	return out, nil
}

// procSockets reads both TCP tables. A host without IPv6 has no tcp6 table.
func procSockets() ([]procSocket, error) {
	var out []procSocket
	for _, name := range []string{"tcp", "tcp6"} {
		data, err := os.ReadFile(filepath.Join(procRoot, "net", name))
		if err != nil {
			if name == "tcp6" && errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, err
		}
		out = append(out, parseProcNetTCP(data)...)
	}
	return out, nil
}

// socketHolders maps each wanted socket inode to the processes holding it.
func socketHolders(wanted map[uint64]bool) (map[uint64][]int, error) {
	out := map[uint64][]int{}
	if len(wanted) == 0 {
		return out, nil
	}
	pids, err := livePIDs()
	if err != nil {
		return nil, err
	}
	self := os.Getpid()
	for _, pid := range pids {
		if pid == self {
			continue
		}
		held := map[uint64]bool{}
		forEachFD(pid, func(_, target string) {
			inode, ok := linkInode(target, "socket:[")
			if ok && wanted[inode] && !held[inode] {
				held[inode] = true
				out[inode] = append(out[inode], pid)
			}
		})
	}
	return out, nil
}

func sameEndpoint(a, b netip.AddrPort) bool {
	return a.Port() == b.Port() && a.Addr().Unmap() == b.Addr().Unmap()
}

func livePIDs() ([]int, error) {
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return nil, err
	}
	pids := make([]int, 0, len(entries))
	for _, e := range entries {
		if pid, err := strconv.Atoi(e.Name()); err == nil && pid > 0 {
			pids = append(pids, pid)
		}
	}
	return pids, nil
}

// forEachFD visits a process's descriptors. A process the host may not
// inspect, or one that exits mid-walk, contributes nothing.
func forEachFD(pid int, visit func(fd, target string)) {
	dir := filepath.Join(procRoot, strconv.Itoa(pid), "fd")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		target, err := os.Readlink(filepath.Join(dir, e.Name()))
		if err == nil {
			visit(e.Name(), target)
		}
	}
}

// linkInode reads the inode from a descriptor link such as "pipe:[123]".
func linkInode(target, prefix string) (uint64, bool) {
	rest, ok := strings.CutPrefix(target, prefix)
	if !ok {
		return 0, false
	}
	rest, ok = strings.CutSuffix(rest, "]")
	if !ok {
		return 0, false
	}
	inode, err := strconv.ParseUint(rest, 10, 64)
	return inode, err == nil
}

// fdWritable reports whether a descriptor was opened for writing.
func fdWritable(pid int, fd string) bool {
	data, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "fdinfo", fd))
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
