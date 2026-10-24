//go:build !linux

package gitcredential

import (
	"errors"
	"net"
)

func verifyPeer(*net.UnixConn, string, string, servedProcess) error {
	return errors.New("git credential service peers are verified only on Linux")
}
