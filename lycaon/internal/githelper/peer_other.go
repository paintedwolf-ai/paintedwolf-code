//go:build !linux && !darwin

package githelper

import (
	"errors"
	"net"
	"syscall"
)

func enableListenerCredentials(string, string, syscall.RawConn) error { return nil }

func WriteIdentified(conn *net.UnixConn, data []byte) error {
	_, err := conn.Write(data)
	return err
}

func verifyPeer(*net.UnixConn, string, string, ServedProcess) ([]byte, error) {
	return nil, errors.New("git helper peers are verified only on Linux and macOS")
}
