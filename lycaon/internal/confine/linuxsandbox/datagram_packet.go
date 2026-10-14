package linuxsandbox

import (
	"encoding/binary"
	"net/netip"
)

// The gateway interface carries bare IP packets. The outer helper reads the
// sandbox's UDP datagrams from it and writes the replies back as packets it
// builds; every other packet is dropped.

const (
	ipv4HeaderLen = 20
	ipv6HeaderLen = 40
	udpHeaderLen  = 8
	protocolUDP   = 17
	// packetTTL is the hop limit of the replies the gateway writes.
	packetTTL = 64
)

// datagram is one UDP datagram crossing the gateway.
type datagram struct {
	src, dst netip.AddrPort
	payload  []byte
}

// parseDatagram reads a UDP datagram from an IP packet. Fragments, IPv6
// extension headers, and other protocols are not datagrams the gateway carries.
func parseDatagram(packet []byte) (datagram, bool) {
	if len(packet) < 1 {
		return datagram{}, false
	}
	var src, dst netip.Addr
	var body []byte
	switch packet[0] >> 4 {
	case 4:
		if len(packet) < ipv4HeaderLen {
			return datagram{}, false
		}
		ihl := int(packet[0]&0x0f) * 4
		total := int(binary.BigEndian.Uint16(packet[2:4]))
		flagsOffset := binary.BigEndian.Uint16(packet[6:8])
		// More-fragments set, or a nonzero offset, marks a fragment.
		if ihl < ipv4HeaderLen || total < ihl || total > len(packet) || flagsOffset&0x3fff != 0 || packet[9] != protocolUDP {
			return datagram{}, false
		}
		src = netip.AddrFrom4([4]byte(packet[12:16]))
		dst = netip.AddrFrom4([4]byte(packet[16:20]))
		body = packet[ihl:total]
	case 6:
		if len(packet) < ipv6HeaderLen {
			return datagram{}, false
		}
		length := int(binary.BigEndian.Uint16(packet[4:6]))
		if packet[6] != protocolUDP || ipv6HeaderLen+length > len(packet) {
			return datagram{}, false
		}
		src = netip.AddrFrom16([16]byte(packet[8:24]))
		dst = netip.AddrFrom16([16]byte(packet[24:40]))
		body = packet[ipv6HeaderLen : ipv6HeaderLen+length]
	default:
		return datagram{}, false
	}
	if len(body) < udpHeaderLen {
		return datagram{}, false
	}
	length := int(binary.BigEndian.Uint16(body[4:6]))
	if length < udpHeaderLen || length > len(body) {
		return datagram{}, false
	}
	return datagram{
		src:     netip.AddrPortFrom(src, binary.BigEndian.Uint16(body[0:2])),
		dst:     netip.AddrPortFrom(dst, binary.BigEndian.Uint16(body[2:4])),
		payload: body[udpHeaderLen:length],
	}, true
}

// buildDatagram renders d as an IP packet with valid checksums. The two
// addresses must be of one family.
func buildDatagram(d datagram) []byte {
	udpLen := udpHeaderLen + len(d.payload)
	var packet []byte
	var udp []byte
	if d.src.Addr().Is4() {
		packet = make([]byte, ipv4HeaderLen+udpLen)
		packet[0] = 0x45
		binary.BigEndian.PutUint16(packet[2:4], uint16(len(packet)))
		// Don't fragment: the interface carries any datagram whole.
		binary.BigEndian.PutUint16(packet[6:8], 0x4000)
		packet[8] = packetTTL
		packet[9] = protocolUDP
		src, dst := d.src.Addr().As4(), d.dst.Addr().As4()
		copy(packet[12:16], src[:])
		copy(packet[16:20], dst[:])
		binary.BigEndian.PutUint16(packet[10:12], checksum(packet[:ipv4HeaderLen], 0))
		udp = packet[ipv4HeaderLen:]
	} else {
		packet = make([]byte, ipv6HeaderLen+udpLen)
		packet[0] = 0x60
		binary.BigEndian.PutUint16(packet[4:6], uint16(udpLen))
		packet[6] = protocolUDP
		packet[7] = packetTTL
		src, dst := d.src.Addr().As16(), d.dst.Addr().As16()
		copy(packet[8:24], src[:])
		copy(packet[24:40], dst[:])
		udp = packet[ipv6HeaderLen:]
	}
	binary.BigEndian.PutUint16(udp[0:2], d.src.Port())
	binary.BigEndian.PutUint16(udp[2:4], d.dst.Port())
	binary.BigEndian.PutUint16(udp[4:6], uint16(udpLen))
	copy(udp[udpHeaderLen:], d.payload)
	sum := checksum(udp, pseudoHeaderSum(d.src.Addr(), d.dst.Addr(), udpLen))
	if sum == 0 {
		// Zero means no checksum; its ones'-complement twin carries it.
		sum = 0xffff
	}
	binary.BigEndian.PutUint16(udp[6:8], sum)
	return packet
}

// pseudoHeaderSum is the unfolded sum of the UDP pseudo-header.
func pseudoHeaderSum(src, dst netip.Addr, length int) uint32 {
	var sum uint32
	add := func(b []byte) {
		for i := 0; i+1 < len(b); i += 2 {
			sum += uint32(binary.BigEndian.Uint16(b[i:]))
		}
	}
	if src.Is4() {
		s, d := src.As4(), dst.As4()
		add(s[:])
		add(d[:])
	} else {
		s, d := src.As16(), dst.As16()
		add(s[:])
		add(d[:])
	}
	return sum + protocolUDP + uint32(length)
}

// checksum is the Internet checksum of b, starting from an unfolded sum.
func checksum(b []byte, sum uint32) uint16 {
	for i := 0; i+1 < len(b); i += 2 {
		sum += uint32(binary.BigEndian.Uint16(b[i:]))
	}
	if len(b)%2 == 1 {
		sum += uint32(b[len(b)-1]) << 8
	}
	for sum > 0xffff {
		sum = sum>>16 + sum&0xffff
	}
	return ^uint16(sum)
}
