//go:build darwin || linux

package privateguesthost

import (
	"net"
	"os"
)

func activationChannel() (net.Conn, *os.File, error) {
	pair, err := activationDescriptors()
	if err != nil {
		return nil, nil, err
	}
	parent := os.NewFile(uintptr(pair[0]), "private service host")
	child := os.NewFile(uintptr(pair[1]), "private service activation")
	conn, err := net.FileConn(parent)
	_ = parent.Close()
	if err != nil {
		_ = child.Close()
		return nil, nil, err
	}
	return conn, child, nil
}
