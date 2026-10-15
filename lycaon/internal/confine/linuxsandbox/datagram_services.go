package linuxsandbox

import (
	"encoding/binary"
	"encoding/hex"
	"net/netip"
	"strconv"
	"strings"
)

// boundDatagramSockets reads the bound sockets a /proc/net/udp or udp6 table
// lists, by local address.
func boundDatagramSockets(table string) []netip.AddrPort {
	var out []netip.AddrPort
	for _, line := range strings.Split(table, "\n") {
		fields := strings.Fields(line)
		// 07 is the state of a bound datagram socket.
		if len(fields) < 4 || fields[3] != "07" {
			continue
		}
		addrText, portText, ok := strings.Cut(fields[1], ":")
		if !ok {
			continue
		}
		port, err := strconv.ParseUint(portText, 16, 16)
		if err != nil {
			continue
		}
		raw, err := hex.DecodeString(addrText)
		if err != nil || (len(raw) != 4 && len(raw) != 16) {
			continue
		}
		// The kernel prints each 32-bit word of the address in host order.
		for i := 0; i+4 <= len(raw); i += 4 {
			binary.BigEndian.PutUint32(raw[i:], binary.NativeEndian.Uint32(raw[i:]))
		}
		addr, _ := netip.AddrFromSlice(raw)
		out = append(out, netip.AddrPortFrom(addr.Unmap(), uint16(port)))
	}
	return out
}

// loopbackServiceAddrs are the sandbox addresses that reach the host's
// services among bound: a loopback socket by its own address, a wildcard one
// by its family's loopback address. Ports from ephemeralFloor up are a
// client's source ports, not services.
func loopbackServiceAddrs(bound []netip.AddrPort, ephemeralFloor uint16) []netip.AddrPort {
	seen := map[netip.AddrPort]bool{}
	var out []netip.AddrPort
	for _, b := range bound {
		if b.Port() == 0 || b.Port() >= ephemeralFloor {
			continue
		}
		addr := b.Addr()
		switch {
		case addr.IsLoopback():
		case addr.IsUnspecified():
			addr = loopbackOf(addr)
		default:
			continue
		}
		a := netip.AddrPortFrom(addr, b.Port())
		if !seen[a] {
			seen[a] = true
			out = append(out, a)
		}
	}
	return out
}

// hostServes reports whether bound holds a socket that receives datagrams
// sent to dst on the host.
func hostServes(bound []netip.AddrPort, dst netip.AddrPort) bool {
	for _, b := range bound {
		if b.Port() != dst.Port() {
			continue
		}
		if b.Addr() == dst.Addr() || (b.Addr().IsUnspecified() && (b.Addr().Is6() || dst.Addr().Is4())) {
			return true
		}
	}
	return false
}

// ephemeralFloor reads the first port of ip_local_port_range.
func ephemeralFloor(portRange string) uint16 {
	fields := strings.Fields(portRange)
	if len(fields) > 0 {
		if v, err := strconv.ParseUint(fields[0], 10, 16); err == nil && v > 0 {
			return uint16(v)
		}
	}
	return 32768
}
