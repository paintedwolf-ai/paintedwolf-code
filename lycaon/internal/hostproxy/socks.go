package hostproxy

import (
	"encoding/binary"
	"io"
	"net"
	"net/netip"
	"net/url"
)

// Both SOCKS names carry the already-authorized numeric IP, never a hostname
// the proxy could resolve differently from the authorization boundary.
func socks(connection net.Conn, credentials *url.Userinfo, address netip.AddrPort) error {
	method := byte(0)
	if credentials != nil {
		method = 2
	}
	if err := writePacket(connection, []byte{5, 1, method}); err != nil {
		return ProxyUnavailable
	}
	var choice [2]byte
	if _, err := io.ReadFull(connection, choice[:]); err != nil {
		return ProxyProtocol
	}
	if choice != [2]byte{5, method} {
		return ProxyRejected
	}
	if credentials != nil {
		user := credentials.Username()
		password, _ := credentials.Password()
		if len(user) == 0 || len(user) > 255 || len(password) > 255 {
			return EndpointInvalid
		}
		auth := append([]byte{1, byte(len(user))}, user...)
		auth = append(auth, byte(len(password)))
		auth = append(auth, password...)
		if err := writePacket(connection, auth); err != nil {
			return ProxyUnavailable
		}
		if _, err := io.ReadFull(connection, choice[:]); err != nil {
			return ProxyProtocol
		}
		if choice != [2]byte{1, 0} {
			return ProxyRejected
		}
	}
	ip := address.Addr().Unmap()
	kind := byte(4)
	if ip.Is4() {
		kind = 1
	}
	request := append([]byte{5, 1, 0, kind}, ip.AsSlice()...)
	request = binary.BigEndian.AppendUint16(request, address.Port())
	if err := writePacket(connection, request); err != nil {
		return ProxyUnavailable
	}
	var reply [4]byte
	if _, err := io.ReadFull(connection, reply[:]); err != nil {
		return ProxyProtocol
	}
	if reply[0] != 5 || reply[2] != 0 {
		return ProxyProtocol
	}
	if reply[1] != 0 {
		return ProxyRejected
	}
	count := 0
	switch reply[3] {
	case 1:
		count = 4
	case 4:
		count = 16
	case 3:
		var length [1]byte
		if _, err := io.ReadFull(connection, length[:]); err != nil {
			return ProxyProtocol
		}
		count = int(length[0])
	default:
		return ProxyProtocol
	}
	var bound [257]byte
	if _, err := io.ReadFull(connection, bound[:count+2]); err != nil {
		return ProxyProtocol
	}
	return nil
}
