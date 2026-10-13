//go:build linux

package main

import (
	"errors"
	"net"

	"golang.org/x/sys/unix"
)

// peerCredentials reads the connecting process's credentials from the
// kernel, which no client can forge.
func peerCredentials(conn net.Conn) (Peer, error) {
	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		return Peer{}, errors.New("not a Unix socket")
	}
	raw, err := unixConn.SyscallConn()
	if err != nil {
		return Peer{}, err
	}
	var cred *unix.Ucred
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		cred, credErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return Peer{}, err
	}
	if credErr != nil {
		return Peer{}, credErr
	}
	return Peer{UID: int(cred.Uid), PID: int(cred.Pid)}, nil
}
