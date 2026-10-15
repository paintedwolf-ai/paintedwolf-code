//go:build !linux

package main

import (
	"errors"
	"net"
)

// peerCredentials exist only on Linux; other platforms build the service
// for its tests.
func peerCredentials(net.Conn) (Peer, error) {
	return Peer{}, errors.New("peer credentials are available on Linux only")
}
