package lineage

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"net/netip"
	"strconv"
	"strings"
)

// procTCPListen is the kernel's TCP_LISTEN state as /proc/net/tcp prints it.
const procTCPListen = "0A"

// procSocket is one row of /proc/net/tcp or /proc/net/tcp6.
type procSocket struct {
	Local     netip.AddrPort
	Remote    netip.AddrPort
	Listening bool
	Inode     uint64
}

// parseProcNetTCP reads the kernel TCP table. Rows it cannot decode are
// skipped rather than guessed.
func parseProcNetTCP(data []byte) []procSocket {
	var out []procSocket
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 10 || fields[0] == "sl" {
			continue
		}
		local, ok := parseProcAddrPort(fields[1])
		if !ok {
			continue
		}
		remote, ok := parseProcAddrPort(fields[2])
		if !ok {
			continue
		}
		inode, err := strconv.ParseUint(fields[9], 10, 64)
		if err != nil {
			continue
		}
		out = append(out, procSocket{Local: local, Remote: remote, Listening: fields[3] == procTCPListen, Inode: inode})
	}
	return out
}

// parseProcAddrPort decodes "ADDR:PORT", where ADDR is 8 (IPv4) or 32 (IPv6)
// hex digits of 32-bit words in host byte order and PORT is big-endian hex.
func parseProcAddrPort(field string) (netip.AddrPort, bool) {
	addrHex, portHex, ok := strings.Cut(field, ":")
	if !ok {
		return netip.AddrPort{}, false
	}
	port, err := strconv.ParseUint(portHex, 16, 16)
	if err != nil {
		return netip.AddrPort{}, false
	}
	if len(addrHex) != 8 && len(addrHex) != 32 {
		return netip.AddrPort{}, false
	}
	raw := make([]byte, 0, 16)
	for i := 0; i < len(addrHex); i += 8 {
		word, err := strconv.ParseUint(addrHex[i:i+8], 16, 32)
		if err != nil {
			return netip.AddrPort{}, false
		}
		raw = binary.NativeEndian.AppendUint32(raw, uint32(word))
	}
	addr, ok := netip.AddrFromSlice(raw)
	if !ok {
		return netip.AddrPort{}, false
	}
	return netip.AddrPortFrom(addr, uint16(port)), true
}
