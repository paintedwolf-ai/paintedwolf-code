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
	mu        sync.Mutex
	endpoints map[object]map[object]endpoint
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
		unix.Close(dir)
		return nil, err
	}
	pair, err := socketPair()
	if err != nil {
		unix.Close(dir)
		return nil, err
	}
	file := os.NewFile(uintptr(pair[0]), "session socket registry")
	channel, err := net.FileConn(file)
	_ = file.Close()
	if err != nil {
		unix.Close(dir)
		unix.Close(pair[1])
		return nil, err
	}
	go func() {
		defer unix.Close(dir)
		defer channel.Close()
		r.serve(channel.(*net.UnixConn), session)
	}()
	return os.NewFile(uintptr(pair[1]), "session sockets"), nil
}

func (r *Registry) serve(channel *net.UnixConn, session object) {
	owned := make(map[object]int)
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
			unix.Close(fd)
		}
	}
}

func (r *Registry) request(session object, owned map[object]int, op byte, fd int) unix.Errno {
	if op == register {
		// SIOCUNIXFILE returns the actual bound filesystem socket.
		handle, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), 0x89e0, 0)
		if errno != 0 {
			return errno
		}
		return r.retain(session, owned, int(handle))
	}
	if op != lookup {
		return unix.EINVAL
	}
	id, err := identify(fd, unix.S_IFSOCK)
	if err != nil {
		return unix.EINVAL
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, admitted := r.endpoints[session][id]; !admitted {
		return unix.EACCES
	}
	return 0
}

// One session may keep this many named socket inodes pinned in the engine.
const maxSessionSockets = 256

type endpoint struct {
	fd    int
	owner map[object]int
}

func (r *Registry) retain(session object, owned map[object]int, fd int) unix.Errno {
	id, err := identify(fd, unix.S_IFSOCK)
	if err != nil {
		unix.Close(fd)
		return unix.EINVAL
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.endpoints == nil {
		r.endpoints = make(map[object]map[object]endpoint)
	}
	group := r.endpoints[session]
	if group == nil {
		group = make(map[object]endpoint)
		r.endpoints[session] = group
	}
	if _, exists := group[id]; exists {
		unix.Close(fd)
		return unix.EEXIST
	}
	if len(group) >= maxSessionSockets {
		for id, e := range group {
			var stat unix.Stat_t
			if unix.Fstat(e.fd, &stat) == nil && stat.Nlink == 0 {
				delete(e.owner, id)
				delete(group, id)
				unix.Close(e.fd)
			}
		}
		if len(group) >= maxSessionSockets {
			unix.Close(fd)
			return unix.ENOSPC
		}
	}
	group[id] = endpoint{fd: fd, owner: owned}
	owned[id] = fd
	return 0
}

func (r *Registry) release(session object, owned map[object]int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, fd := range owned {
		delete(r.endpoints[session], id)
		unix.Close(fd)
	}
	if len(r.endpoints[session]) == 0 {
		delete(r.endpoints, session)
	}
}
