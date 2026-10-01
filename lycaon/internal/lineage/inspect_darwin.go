//go:build darwin && cgo

package lineage

/*
#include <libproc.h>
#include <sys/proc_info.h>
#include <arpa/inet.h>
#include <errno.h>
#include <stdlib.h>
#include <string.h>

// pw_pipe_peer reads the kernel identity of the pipe end joined to fd.
static int pw_pipe_peer(int pid, int fd, uint64_t *peer) {
 struct pipe_fdinfo info;
 int n = proc_pidfdinfo(pid, fd, PROC_PIDFDPIPEINFO, &info, sizeof(info));
 if (n != (int)sizeof(info)) return errno ? errno : ENOTSUP;
 *peer = info.pipeinfo.pipe_peerhandle;
 return 0;
}

// pw_pipe_matches reports which of the wanted pipe ends pid holds, as a bitmap
// over wanted[0..count). One walk of a process answers for every lineage.
static int pw_pipe_matches(int pid, const uint64_t *wanted, int count, uint64_t *found) {
 *found = 0;
 int size = proc_pidinfo(pid, PROC_PIDLISTFDS, 0, NULL, 0);
 if (size <= 0) return 0;
 struct proc_fdinfo *fds = (struct proc_fdinfo *)malloc((size_t)size);
 if (fds == NULL) return 0;
 int n = proc_pidinfo(pid, PROC_PIDLISTFDS, 0, fds, size);
 int fdcount = n > 0 ? n / (int)sizeof(struct proc_fdinfo) : 0;
 for (int i = 0; i < fdcount; i++) {
  if (fds[i].proc_fdtype != PROX_FDTYPE_PIPE) continue;
  struct pipe_fdinfo info;
  if (proc_pidfdinfo(pid, fds[i].proc_fd, PROC_PIDFDPIPEINFO, &info, sizeof(info)) != (int)sizeof(info)) continue;
  for (int w = 0; w < count; w++) {
   if (info.pipeinfo.pipe_handle == wanted[w]) *found |= (uint64_t)1 << w;
  }
 }
 free(fds);
 return *found != 0;
}

// pw_owns_socket reports whether pid holds the TCP socket with these ports.
static int pw_owns_socket(int pid, uint16_t lport, uint16_t fport) {
 int size = proc_pidinfo(pid, PROC_PIDLISTFDS, 0, NULL, 0);
 if (size <= 0) return 0;
 struct proc_fdinfo *fds = (struct proc_fdinfo *)malloc((size_t)size);
 if (fds == NULL) return 0;
 int n = proc_pidinfo(pid, PROC_PIDLISTFDS, 0, fds, size);
 int count = n > 0 ? n / (int)sizeof(struct proc_fdinfo) : 0;
 int found = 0;
 for (int i = 0; i < count && !found; i++) {
  if (fds[i].proc_fdtype != PROX_FDTYPE_SOCKET) continue;
  struct socket_fdinfo info;
  // The kernel may return fewer bytes than the struct declares; anything that
  // covers the address block is enough to match a connection.
  int n2 = proc_pidfdinfo(pid, fds[i].proc_fd, PROC_PIDFDSOCKETINFO, &info, sizeof(info));
  if (n2 <= 0) continue;
  if (info.psi.soi_kind != SOCKINFO_TCP) continue;
  struct in_sockinfo *in = &info.psi.soi_proto.pri_tcp.tcpsi_ini;
  if (ntohs((uint16_t)in->insi_lport) == lport && ntohs((uint16_t)in->insi_fport) == fport) found = 1;
 }
 free(fds);
 return found;
}
*/
import "C"

import (
	"net/netip"
	"os"
	"syscall"
	"unsafe"
)

func pipePeerHandle(read *os.File) (uint64, error) {
	conn, err := read.SyscallConn()
	if err != nil {
		return 0, err
	}
	var peer C.uint64_t
	var code C.int
	ctlErr := conn.Control(func(fd uintptr) {
		code = C.pw_pipe_peer(C.int(os.Getpid()), C.int(fd), &peer)
	})
	if ctlErr != nil {
		return 0, ctlErr
	}
	if code != 0 {
		return 0, syscall.Errno(code)
	}
	return uint64(peer), nil
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
		n, _, errno := syscall.Syscall(syscall.SYS_READ, fd, uintptr(unsafe.Pointer(&buf[0])), 1)
		// End of file means every write end closed; anything else means one is open.
		writers = !(errno == 0 && n == 0)
	})
	return writers
}

// maxScanGroup bounds one pass; the bitmap carries 64 lineages at a time.
const maxScanGroup = 64

// pipeHolders answers for many lineages in one walk of the process table,
// because attribution asks about all of them at once and a second walk costs
// as much as the first.
func pipeHolders(handles []uint64) ([][]int, error) {
	out := make([][]int, len(handles))
	if len(handles) == 0 {
		return out, nil
	}
	pids, err := livePIDs()
	if err != nil {
		return nil, err
	}
	self := os.Getpid()
	for start := 0; start < len(handles); start += maxScanGroup {
		end := min(start+maxScanGroup, len(handles))
		group := handles[start:end]
		cgroup := make([]C.uint64_t, len(group))
		for i, h := range group {
			cgroup[i] = C.uint64_t(h)
		}
		for _, pid := range pids {
			if pid <= 0 || pid == self {
				continue
			}
			var found C.uint64_t
			if C.pw_pipe_matches(C.int(pid), &cgroup[0], C.int(len(cgroup)), &found) == 0 {
				continue
			}
			for i := range group {
				if uint64(found)&(uint64(1)<<i) != 0 {
					out[start+i] = append(out[start+i], pid)
				}
			}
		}
	}
	return out, nil
}

func peerPID(local, remote netip.AddrPort) (int, bool) {
	pids, err := livePIDs()
	if err != nil {
		return 0, false
	}
	self := os.Getpid()
	for _, pid := range pids {
		if pid <= 0 || pid == self {
			continue
		}
		// The peer's local port is the connection's remote port.
		if C.pw_owns_socket(C.int(pid), C.uint16_t(remote.Port()), C.uint16_t(local.Port())) != 0 {
			return pid, true
		}
	}
	return 0, false
}

func livePIDs() ([]int, error) {
	n := int(C.proc_listallpids(nil, 0))
	if n <= 0 {
		return nil, ErrUnsupported
	}
	pids := make([]C.int, n+1024)
	count := int(C.proc_listallpids(unsafe.Pointer(&pids[0]), C.int(len(pids)*4)))
	if count <= 0 || count >= len(pids) {
		return nil, ErrUnsupported
	}
	out := make([]int, count)
	for i := range out {
		out[i] = int(pids[i])
	}
	return out, nil
}

func listeners() ([]Listener, error) { return nil, ErrUnsupported }

const supported = true
