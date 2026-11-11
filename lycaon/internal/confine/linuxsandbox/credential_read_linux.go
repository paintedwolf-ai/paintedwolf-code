//go:build linux

package linuxsandbox

import (
	"bytes"
	"context"
	"crypto/rand"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"golang.org/x/sys/unix"
)

// connectCredentialRead preserves the target's socket description (including
// duplicates) while attaching it to a private, read-mediated protocol endpoint.
// A challenge written through that same description binds the accepted peer;
// an outside process racing the temporary listener cannot gain a bus connection.
func (s *supervisor) connectCredentialRead(socket int, reference *os.File) unix.Errno {
	if s.credentials == nil {
		return unix.EACCES
	}
	directory, err := os.MkdirTemp("/tmp", "pw-credential-")
	if err != nil {
		return unix.EACCES
	}
	defer func() { _ = os.RemoveAll(directory) }()
	path := filepath.Join(directory, "bus")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return unix.EACCES
	}
	defer func() { _ = listener.Close() }()
	deadline := time.Now().Add(500 * time.Millisecond)
	if err := listener.SetDeadline(deadline); err != nil {
		return unix.EACCES
	}
	if err := unix.Connect(socket, &unix.SockaddrUnix{Name: path}); err != nil {
		return errnoOf(err, unix.EACCES)
	}
	var challenge [32]byte
	if _, err := rand.Read(challenge[:]); err != nil {
		return unix.EACCES
	}
	if n, err := unix.Write(socket, challenge[:]); err != nil || n != len(challenge) {
		return unix.EACCES
	}
	inside, err := listener.AcceptUnix()
	if err != nil {
		return unix.EACCES
	}
	owned := true
	defer func() {
		if owned {
			_ = inside.Close()
		}
	}()
	if err := inside.SetDeadline(deadline); err != nil {
		return unix.EACCES
	}
	var received [32]byte
	if _, err := io.ReadFull(inside, received[:]); err != nil || !bytes.Equal(received[:], challenge[:]) {
		return unix.EACCES
	}
	if err := inside.SetDeadline(time.Time{}); err != nil {
		return unix.EACCES
	}
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	upstream, err := (&net.Dialer{}).DialContext(ctx, "unix", "/proc/self/fd/"+strconv.Itoa(int(reference.Fd())))
	if err != nil {
		return unix.EACCES
	}
	owned = false
	if err := s.credentials.Start(inside, upstream); err != nil {
		return unix.EACCES
	}
	return 0
}
