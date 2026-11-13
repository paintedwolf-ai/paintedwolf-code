//go:build !linux && !darwin

package gitcredential

import (
	"errors"
	"net"
)

func writeIdentified(conn *net.UnixConn, data []byte) error {
	_, err := conn.Write(data)
	return err
}

func verifyPeer(*net.UnixConn, string, string, servedProcess) ([]byte, error) {
	return nil, errors.New("git credential service peers are verified only on Linux and macOS")
}
