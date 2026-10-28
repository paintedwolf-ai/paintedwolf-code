package lineage

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"io/fs"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// procTCPListen is the kernel's TCP_LISTEN state as /proc/net/tcp prints it.
const procTCPListen = "0A"

// procUDPUnconnected is TCP_CLOSE, which an unconnected bound UDP socket reports.
const procUDPUnconnected = "07"

// procSocket is one row of a /proc/net socket table.
type procSocket struct {
	Protocol  Protocol
	Local     netip.AddrPort
	Remote    netip.AddrPort
	Listening bool
	UID       uint32
	Inode     uint64
}

// procFS reads Linux kernel tables under one procfs mount. The host reads
// /proc; tests read a fixture tree with the same layout.
type procFS struct{ root string }

// procTables names each protocol's tables. A host without IPv6 has no *6 table.
var procTables = map[Protocol][]string{ProtocolTCP: {"tcp", "tcp6"}, ProtocolUDP: {"udp", "udp6"}}

// sockets reads the tables of each wanted protocol.
func (p procFS) sockets(protocols []Protocol) ([]procSocket, error) {
	var out []procSocket
	for _, protocol := range protocols {
		for _, name := range procTables[protocol] {
			data, err := os.ReadFile(filepath.Join(p.root, "net", name))
			if err != nil {
				if strings.HasSuffix(name, "6") && errors.Is(err, fs.ErrNotExist) {
					continue
				}
				return nil, err
			}
			out = append(out, parseProcNet(data, protocol)...)
		}
	}
	return out, nil
}

// listeners joins the wanted listening sockets with the processes holding
// them. A socket whose holders the host cannot read keeps an empty PID list.
func (p procFS) listeners(query ListenerQuery) ([]Listener, error) {
	sockets, err := p.sockets(query.protocols())
	if err != nil {
		return nil, err
	}
	var rows []kernelListener
	for _, s := range sockets {
		if s.Listening && query.wants(s.Protocol, s.Local) {
			rows = append(rows, kernelListener{Protocol: s.Protocol, Local: s.Local, UID: s.UID, Handle: s.Inode})
		}
	}
	if len(rows) == 0 {
		return nil, nil
	}
	pids, err := p.livePIDs()
	if err != nil {
		return nil, err
	}
	return resolveListeners(rows, pids, p.socketInodes), nil
}

// socketHolders maps each wanted socket inode to the processes holding it.
func (p procFS) socketHolders(wanted map[uint64]bool) (map[uint64][]int, error) {
	if len(wanted) == 0 {
		return map[uint64][]int{}, nil
	}
	pids, err := p.livePIDs()
	if err != nil {
		return nil, err
	}
	return socketHolders(wanted, pids, p.socketInodes), nil
}

// socketInodes lists the socket inodes a process holds; an unreadable
// descriptor table yields none.
func (p procFS) socketInodes(pid int) []uint64 {
	var out []uint64
	p.forEachFD(pid, func(_, target string) {
		if inode, ok := linkInode(target, "socket:["); ok {
			out = append(out, inode)
		}
	})
	return out
}

func (p procFS) livePIDs() ([]int, error) {
	entries, err := os.ReadDir(p.root)
	if err != nil {
		return nil, err
	}
	pids := make([]int, 0, len(entries))
	for _, e := range entries {
		if pid, err := strconv.Atoi(e.Name()); err == nil && pid > 0 {
			pids = append(pids, pid)
		}
	}
	return pids, nil
}

// forEachFD visits a process's descriptors. A process the host may not
// inspect, or one that exits mid-walk, contributes nothing.
func (p procFS) forEachFD(pid int, visit func(fd, target string)) {
	dir := filepath.Join(p.root, strconv.Itoa(pid), "fd")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		target, err := os.Readlink(filepath.Join(dir, e.Name()))
		if err == nil {
			visit(e.Name(), target)
		}
	}
}

// linkInode reads the inode from a descriptor link such as "pipe:[123]".
func linkInode(target, prefix string) (uint64, bool) {
	rest, ok := strings.CutPrefix(target, prefix)
	if !ok {
		return 0, false
	}
	rest, ok = strings.CutSuffix(rest, "]")
	if !ok {
		return 0, false
	}
	inode, err := strconv.ParseUint(rest, 10, 64)
	return inode, err == nil
}

// parseProcNet reads one kernel socket table. A TCP row listens in
// TCP_LISTEN; a UDP row listens while bound and unconnected. Rows it cannot
// decode are skipped rather than guessed.
func parseProcNet(data []byte, protocol Protocol) []procSocket {
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
		uid, err := strconv.ParseUint(fields[7], 10, 32)
		if err != nil {
			continue
		}
		inode, err := strconv.ParseUint(fields[9], 10, 64)
		if err != nil {
			continue
		}
		listening := fields[3] == procTCPListen
		if protocol == ProtocolUDP {
			listening = fields[3] == procUDPUnconnected && local.Port() != 0 && remote.Port() == 0
		}
		out = append(out, procSocket{Protocol: protocol, Local: local, Remote: remote, Listening: listening, UID: uint32(uid), Inode: inode})
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
