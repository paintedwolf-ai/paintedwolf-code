//go:build linux

package linuxsandbox

import (
	"fmt"
	"os"
	"strconv"
	"sync"

	"golang.org/x/sys/unix"
)

// OwnSocketReferences validates the outer helper's inherited destinations and
// prevents their inheritance by the inner helper or target.
func OwnSocketReferences(network Network) (func(), error) {
	if err := (Plan{Version: PlanVersion, Network: network}).Validate(); err != nil {
		return nil, err
	}
	files := make([]*os.File, 0, len(network.SocketGrants))
	closeFiles := func() {
		for _, file := range files {
			_ = file.Close()
		}
	}
	for _, grant := range network.SocketGrants {
		var stat unix.Stat_t
		flags, err := unix.FcntlInt(uintptr(grant.FD), unix.F_GETFL, 0)
		if err != nil || flags&unix.O_PATH == 0 || unix.Fstat(grant.FD, &stat) != nil || stat.Mode&unix.S_IFMT != unix.S_IFSOCK {
			closeFiles()
			return nil, &Unsupported{Code: UnsupportedNetworkGrant, Detail: fmt.Sprintf("socket reference %d is unavailable", grant.FD)}
		}
		unix.CloseOnExec(grant.FD)
		files = append(files, os.NewFile(uintptr(grant.FD), "approved local service"))
	}
	return closeFiles, nil
}

type socketReferences struct {
	mu     sync.Mutex
	files  map[int]*os.File
	closed bool
}

func retainSocketReferences(network Network) (*socketReferences, error) {
	refs := &socketReferences{files: make(map[int]*os.File)}
	for _, grant := range network.SocketGrants {
		if refs.files[grant.FD] != nil {
			refs.close()
			return nil, &Unsupported{Code: UnsupportedNetworkGrant, Detail: "duplicate socket reference"}
		}
		var stat unix.Stat_t
		flags, err := unix.FcntlInt(uintptr(grant.FD), unix.F_GETFL, 0)
		if err != nil || flags&unix.O_PATH == 0 || unix.Fstat(grant.FD, &stat) != nil || stat.Mode&unix.S_IFMT != unix.S_IFSOCK {
			refs.close()
			return nil, &Unsupported{Code: UnsupportedNetworkGrant, Detail: "retained socket reference is unavailable"}
		}
		fd, err := unix.FcntlInt(uintptr(grant.FD), unix.F_DUPFD_CLOEXEC, 0)
		if err != nil {
			refs.close()
			return nil, err
		}
		refs.files[grant.FD] = os.NewFile(uintptr(fd), "supervisor local service")
	}
	return refs, nil
}

func (r *socketReferences) duplicate(id int) (*os.File, error) {
	if r == nil {
		return nil, unix.EACCES
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.files[id] == nil {
		return nil, unix.EACCES
	}
	fd, err := unix.FcntlInt(r.files[id].Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), "operation local service"), nil
}

func (r *socketReferences) close() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	r.closed = true
	for id, file := range r.files {
		_ = file.Close()
		delete(r.files, id)
	}
}

// connectUnix owns its destination independently of supervisor retirement.
func (s *supervisor) connectUnix(socket, referenceID int) unix.Errno {
	reference, err := s.socketRefs.duplicate(referenceID)
	if err != nil {
		return unix.EACCES
	}
	defer func() { _ = reference.Close() }()
	if s.network.beforeSocketConnect != nil {
		s.network.beforeSocketConnect()
	}
	address := &unix.SockaddrUnix{Name: "/proc/self/fd/" + strconv.Itoa(int(reference.Fd()))}
	return errnoOf(unix.Connect(socket, address), 0)
}
