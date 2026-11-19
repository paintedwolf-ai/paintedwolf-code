// Package sessionipc retains the identity of session-created pathname sockets.
package sessionipc

import (
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
	endpoints map[object]map[object]struct{}
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
	go func() {
		defer unix.Close(dir)
		defer unix.Close(pair[0])
		r.serve(pair[0], session)
	}()
	return os.NewFile(uintptr(pair[1]), "session sockets"), nil
}

func (r *Registry) serve(channel int, session object) {
	owned := make(map[object]int)
	defer func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		for id, fd := range owned {
			delete(r.endpoints[session], id)
			unix.Close(fd)
		}
		if len(r.endpoints[session]) == 0 {
			delete(r.endpoints, session)
		}
	}()
	for {
		var data [1]byte
		control := make([]byte, unix.CmsgSpace(2*4))
		n, size, flags, _, err := unix.Recvmsg(channel, data[:], control, unix.MSG_CMSG_CLOEXEC)
		if err == unix.EINTR {
			continue
		}
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
			answer := byte(0)
			if r.request(session, owned, data[0], fds[0]) {
				answer = 1
			}
			_ = unix.Sendmsg(fds[1], []byte{answer}, nil, nil, unix.MSG_NOSIGNAL)
		}
		for _, fd := range fds {
			unix.Close(fd)
		}
	}
}

func (r *Registry) request(session object, owned map[object]int, op byte, fd int) bool {
	if op == register {
		// SIOCUNIXFILE returns the actual bound filesystem socket.
		handle, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), 0x89e0, 0)
		if errno != 0 {
			return false
		}
		bound := int(handle)
		id, err := identify(bound, unix.S_IFSOCK)
		if err != nil {
			unix.Close(bound)
			return false
		}
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.endpoints == nil {
			r.endpoints = make(map[object]map[object]struct{})
		}
		if r.endpoints[session] == nil {
			r.endpoints[session] = make(map[object]struct{})
		}
		if _, exists := r.endpoints[session][id]; exists {
			unix.Close(bound)
			return false
		}
		r.endpoints[session][id] = struct{}{}
		owned[id] = bound
		return true
	}
	if op != lookup {
		return false
	}
	id, err := identify(fd, unix.S_IFSOCK)
	if err != nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	_, admitted := r.endpoints[session][id]
	return admitted
}
