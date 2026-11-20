// Package sessionipc retains the identity of session-created pathname sockets.
package sessionipc

import (
	"encoding/binary"
	"net"
	"os"
	"sync"

	"golang.org/x/sys/unix"
)

type object struct {
	dev uint64
	ino uint64
}

func identify(fd int, kind uint32) (object, error) {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return object{}, err
	}
	if uint32(stat.Mode)&unix.S_IFMT != kind {
		return object{}, unix.EINVAL
	}
	return object{uint64(stat.Dev), stat.Ino}, nil
}

// Registry holds socket inodes while their creating commands are active.
type Registry struct {
	mu     sync.Mutex
	groups map[object]*group
}

// Channel pins the session directory and ties registrations to the launch.
func (r *Registry) Channel(root string) (*os.File, error) {
	dir, err := unix.Openat2(unix.AT_FDCWD, root, &unix.OpenHow{
		Flags: unix.O_PATH | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_NO_SYMLINKS,
	})
	if err != nil {
		return nil, err
	}
	session, err := identify(dir, unix.S_IFDIR)
	if err != nil {
		_ = unix.Close(dir)
		return nil, err
	}
	pair, err := socketPair()
	if err != nil {
		_ = unix.Close(dir)
		return nil, err
	}
	file := os.NewFile(uintptr(pair[0]), "session socket registry")
	channel, err := net.FileConn(file)
	_ = file.Close()
	if err != nil {
		_ = unix.Close(dir)
		_ = unix.Close(pair[1])
		return nil, err
	}
	go func() {
		defer func() { _ = unix.Close(dir) }()
		defer func() { _ = channel.Close() }()
		r.serve(channel.(*net.UnixConn), session)
	}()
	return os.NewFile(uintptr(pair[1]), "session sockets"), nil
}

func (r *Registry) serve(channel *net.UnixConn, session object) {
	owned := &launch{pins: make(map[object]int)}
	defer r.release(session, owned)
	var data [1]byte
	control := make([]byte, unix.CmsgSpace(2*4))
	for {
		n, size, flags, _, err := channel.ReadMsgUnix(data[:], control)
		if err != nil || n == 0 {
			return
		}
		messages, err := unix.ParseSocketControlMessage(control[:size])
		if err != nil {
			return
		}
		var fds []int
		for i := range messages {
			rights, _ := unix.ParseUnixRights(&messages[i])
			fds = append(fds, rights...)
		}
		if len(fds) == 2 && flags&(unix.MSG_CTRUNC|unix.MSG_TRUNC) == 0 {
			errno := r.request(session, owned, data[0], fds[0])
			_ = unix.Sendmsg(fds[1], binary.LittleEndian.AppendUint32(nil, uint32(errno)), nil, nil, unix.MSG_NOSIGNAL)
		}
		for _, fd := range fds {
			_ = unix.Close(fd)
		}
	}
}

func (r *Registry) request(session object, owned *launch, op byte, fd int) unix.Errno {
	switch op {
	case reserve:
		return r.reserve(session, owned)
	case discard:
		r.cancel(session, owned)
		return 0
	case register:
		if !owned.reserved {
			return unix.EINVAL
		}
		defer r.cancel(session, owned)
		// SIOCUNIXFILE returns the inode of the actual bound socket.
		const siocUnixFile = 0x89e0
		handle, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), siocUnixFile, 0)
		if errno != 0 {
			return errno
		}
		return r.retain(session, owned, int(handle))
	case lookup:
		id, err := identify(fd, unix.S_IFSOCK)
		if err != nil {
			return unix.EINVAL
		}
		r.mu.Lock()
		defer r.mu.Unlock()
		if g := r.groups[session]; g != nil {
			if _, ok := g.pins[id]; ok {
				return 0
			}
		}
		return unix.EACCES
	default:
		return unix.EINVAL
	}
}

// One session may keep this many named socket inodes pinned in the engine.
const maxSessionSockets = 256

type group struct {
	pins         map[object]endpoint
	reservations int
}
type launch struct {
	pins     map[object]int
	reserved bool
}
type endpoint struct {
	fd    int
	owner *launch
}

func (r *Registry) reserve(session object, owner *launch) unix.Errno {
	r.mu.Lock()
	defer r.mu.Unlock()
	if owner.reserved {
		return unix.EBUSY
	}
	if r.groups == nil {
		r.groups = make(map[object]*group)
	}
	g := r.groups[session]
	if g == nil {
		g = &group{pins: make(map[object]endpoint)}
		r.groups[session] = g
	}
	if len(g.pins)+g.reservations >= maxSessionSockets {
		g.prune()
		if len(g.pins)+g.reservations >= maxSessionSockets {
			return unix.ENOSPC
		}
	}
	g.reservations++
	owner.reserved = true
	return 0
}

func (g *group) prune() {
	for id, e := range g.pins {
		var stat unix.Stat_t
		if unix.Fstat(e.fd, &stat) == nil && stat.Nlink == 0 {
			delete(e.owner.pins, id)
			delete(g.pins, id)
			_ = unix.Close(e.fd)
		}
	}
}

func (r *Registry) cancel(session object, owner *launch) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if owner.reserved {
		r.groups[session].reservations--
		owner.reserved = false
	}
}

func (r *Registry) retain(session object, owner *launch, fd int) unix.Errno {
	id, err := identify(fd, unix.S_IFSOCK)
	if err != nil {
		_ = unix.Close(fd)
		return unix.EINVAL
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	g := r.groups[session]
	if g == nil || !owner.reserved {
		_ = unix.Close(fd)
		return unix.EINVAL
	}
	if _, exists := g.pins[id]; exists {
		_ = unix.Close(fd)
		return unix.EEXIST
	}
	g.reservations--
	owner.reserved = false
	g.pins[id] = endpoint{fd: fd, owner: owner}
	owner.pins[id] = fd
	return 0
}

func (r *Registry) release(session object, owner *launch) {
	r.mu.Lock()
	defer r.mu.Unlock()
	g := r.groups[session]
	if g == nil {
		return
	}
	if owner.reserved {
		g.reservations--
	}
	for id, fd := range owner.pins {
		delete(g.pins, id)
		_ = unix.Close(fd)
	}
	if len(g.pins) == 0 && g.reservations == 0 {
		delete(r.groups, session)
	}
}
