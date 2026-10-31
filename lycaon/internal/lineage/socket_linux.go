//go:build linux

package lineage

import (
	"encoding/binary"
	"syscall"
)

// SocketsOf joins native TCP and descriptor tables for one current incarnation.
// Inaccessible descriptor tables are failures, never empty ownership evidence.
func SocketsOf(pid int) ([]SocketObservation, error) {
	table, err := hostProc.sockets([]Protocol{ProtocolTCP})
	if err != nil {
		return nil, err
	}
	return hostProc.socketsOf(pid, table)
}

// inet6V6Only queries NETLINK_SOCK_DIAG for per-inode IPV6_V6ONLY (INET_DIAG_SKV6ONLY)
// on AF_INET6 sockets. If netlink is unavailable, callers fall back to bindv6only.
func inet6V6Only(protocol Protocol) map[uint64]bool {
	fd, err := syscall.Socket(syscall.AF_NETLINK, syscall.SOCK_DGRAM|syscall.SOCK_CLOEXEC, syscall.NETLINK_SOCK_DIAG)
	if err != nil {
		return nil
	}
	defer syscall.Close(fd)
	proto := uint8(syscall.IPPROTO_TCP)
	if protocol == ProtocolUDP {
		proto = uint8(syscall.IPPROTO_UDP)
	}
	// nlmsghdr (16 bytes) + inet_diag_req_v2 (56 bytes) = 72 bytes.
	var req [72]byte
	binary.NativeEndian.PutUint32(req[0:4], 72)
	binary.NativeEndian.PutUint16(req[4:6], 20) // SOCK_DIAG_BY_FAMILY
	binary.NativeEndian.PutUint16(req[6:8], syscall.NLM_F_REQUEST|syscall.NLM_F_DUMP)
	req[16] = syscall.AF_INET6
	req[17] = proto
	binary.NativeEndian.PutUint32(req[20:24], 0xffffffff)
	if err := syscall.Sendto(fd, req[:], 0, &syscall.SockaddrNetlink{Family: syscall.AF_NETLINK}); err != nil {
		return nil
	}
	out := map[uint64]bool{}
	buf := make([]byte, 32768)
	for {
		n, _, err := syscall.Recvfrom(fd, buf, 0)
		if err != nil || n <= 0 {
			return out
		}
		msgs, err := syscall.ParseNetlinkMessage(buf[:n])
		if err != nil {
			return out
		}
		for _, m := range msgs {
			if m.Header.Type == syscall.NLMSG_DONE || m.Header.Type == syscall.NLMSG_ERROR {
				return out
			}
			if m.Header.Type != 20 || len(m.Data) < 72 {
				continue
			}
			inode := uint64(binary.NativeEndian.Uint32(m.Data[68:72]))
			attrs := m.Data[72:]
			for len(attrs) >= 4 {
				rtaLen := int(binary.NativeEndian.Uint16(attrs[0:2]))
				rtaType := binary.NativeEndian.Uint16(attrs[2:4])
				if rtaLen < 4 || rtaLen > len(attrs) {
					break
				}
				if rtaType == 9 && rtaLen >= 5 { // INET_DIAG_SKV6ONLY
					out[inode] = attrs[4] != 0
				}
				aligned := (rtaLen + 3) &^ 3
				if aligned > len(attrs) {
					break
				}
				attrs = attrs[aligned:]
			}
		}
	}
}
