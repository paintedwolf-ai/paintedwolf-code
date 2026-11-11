//go:build darwin

package socketref

import (
	"fmt"
	"net"
	"sync"
	"unsafe"

	"golang.org/x/sys/unix"
)

// The retained socket is authority before review. Its first approved connection
// pins the observed process; a later peer change cannot inherit that approval.
func newPeerBinding() func(net.Conn) error {
	var mu sync.Mutex
	var expected [8]uint32
	return func(conn net.Conn) error {
		local, ok := conn.(*net.UnixConn)
		if !ok {
			return ErrUnbound
		}
		raw, err := local.SyscallConn()
		if err != nil {
			return err
		}
		var token [8]uint32
		size := uint32(unsafe.Sizeof(token))
		var code unix.Errno
		if err := raw.Control(func(fd uintptr) {
			_, _, code = unix.Syscall6(unix.SYS_GETSOCKOPT, fd, unix.SOL_LOCAL, unix.LOCAL_PEERTOKEN, uintptr(unsafe.Pointer(&token)), uintptr(unsafe.Pointer(&size)), 0)
		}); err != nil {
			return err
		}
		if code != 0 {
			return code
		}
		if size != uint32(unsafe.Sizeof(token)) || token[5] == 0 || token[7] == 0 {
			return ErrUnbound
		}
		mu.Lock()
		defer mu.Unlock()
		if expected == ([8]uint32{}) {
			expected = token
		}
		if token != expected {
			return fmt.Errorf("local service process changed: %w", ErrUnbound)
		}
		return nil
	}
}
