//go:build linux

package linuxsandbox

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/confine/linuxsandbox/sessionipc"
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
	if network.SessionSocketRoot != "" {
		fd := SocketReferenceFD + len(network.SocketGrants)
		kind, err := unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_TYPE)
		if err != nil || kind != unix.SOCK_SEQPACKET {
			closeFiles()
			return nil, fmt.Errorf("session socket channel unavailable")
		}
		unix.CloseOnExec(fd)
		files = append(files, os.NewFile(uintptr(fd), "session sockets"))
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
	if network.SessionSocketRoot != "" {
		id := SocketReferenceFD + len(network.SocketGrants)
		fd, err := unix.FcntlInt(uintptr(id), unix.F_DUPFD_CLOEXEC, 0)
		if err != nil {
			refs.close()
			return nil, err
		}
		refs.files[id] = os.NewFile(uintptr(fd), "session sockets")
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
	for _, grant := range s.network.SocketGrants {
		if grant.FD == referenceID && grant.CredentialRead {
			return s.connectCredentialRead(socket, reference)
		}
	}
	address := &unix.SockaddrUnix{Name: "/proc/self/fd/" + strconv.Itoa(int(reference.Fd()))}
	return errnoOf(unix.Connect(socket, address), 0)
}

func (s *supervisor) performSessionSocket(target *targetProcess, req SocketRequest, socket int) (int64, unix.Errno) {
	channel, err := s.socketRefs.duplicate(SocketReferenceFD + len(s.network.SocketGrants))
	if err != nil {
		return 0, unix.EACCES
	}
	defer channel.Close()
	if req.Op == OpBind {
		if s.performer == nil {
			return 0, unix.EACCES
		}
		call := pathCall{Op: callSessionBind, Path: req.Unix, NewPath: s.network.SessionSocketRoot,
			Dirs: 2, Notification: target.id, Caller: target.pid, Umask: target.caller.umask}
		if err := s.performer.forward(call, []int{socket, int(channel.Fd())}); err != nil {
			return 0, unix.EACCES
		}
		return 0, errAnswered
	}
	destination, err := unix.Openat2(unix.AT_FDCWD, req.Unix, &unix.OpenHow{Flags: unix.O_PATH | unix.O_CLOEXEC, Resolve: unix.RESOLVE_NO_SYMLINKS})
	if err != nil {
		return 0, errnoOf(err, unix.EACCES)
	}
	defer unix.Close(destination)
	if !sessionipc.Admits(int(channel.Fd()), destination) {
		s.refusals.report(Refusal{Operation: "network-outbound", Target: req.Unix, Process: target.comm()})
		return 0, unix.EACCES
	}
	return 0, errnoOf(unix.Connect(socket, &unix.SockaddrUnix{Name: "/proc/self/fd/" + strconv.Itoa(destination)}), 0)
}

// The performer binds from its pinned parent with the caller's filesystem restrictions.
func (f creationFloors) bindSessionSocket(call pathCall, dirs []int) pathResult {
	if len(dirs) != 2 || call.NewPath == "" {
		return errnoResult(unix.EINVAL)
	}
	p, err := resolveParent(unix.AT_FDCWD, call.Path, unix.RESOLVE_NO_SYMLINKS)
	if err != nil {
		return errnoResult(err)
	}
	defer p.close()
	if !strings.HasPrefix(p.path, call.NewPath+"/") || f.refuses(createEntry, p.path) {
		return pathResult{Errno: int(unix.EACCES), Refusal: &Refusal{Operation: "network-bind", Target: call.Path}}
	}
	cwd, err := unix.Open(".", unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return errnoResult(err)
	}
	defer unix.Close(cwd)
	if err := unix.Fchdir(p.fd); err != nil {
		return errnoResult(err)
	}
	name := p.base
	if name[0] == '@' {
		name = "./" + name
	}
	err = unix.Bind(dirs[0], &unix.SockaddrUnix{Name: name})
	if err := unix.Fchdir(cwd); err != nil {
		return errnoResult(err)
	}
	if err != nil {
		return errnoResult(err)
	}
	if !sessionipc.Register(dirs[1], dirs[0]) {
		return errnoResult(unix.EACCES)
	}
	return pathResult{}
}
