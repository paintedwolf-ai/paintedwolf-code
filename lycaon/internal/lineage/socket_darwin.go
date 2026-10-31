//go:build darwin && cgo

package lineage

/*
#include "socket_darwin.h"
*/
import "C"

import (
	"crypto/sha256"
	"fmt"
	"net/netip"
	"syscall"
	"unsafe"
)

// SocketsOf reads native TCP identities, never a tool's stdout or requested ports.
// An inaccessible or concurrently reused process returns a typed OS failure.
func SocketsOf(pid int) ([]SocketObservation, error) {
	sockets, err := observeSockets(pid)
	if err != nil {
		return nil, err
	}
	return tcpOnly(sockets), nil
}

// observeSockets reads every TCP and UDP socket one current incarnation holds.
func observeSockets(pid int) ([]SocketObservation, error) {
	holder, ok := processIdentity(pid)
	if !ok {
		return nil, syscall.ESRCH
	}
	var rows *C.struct_pw_socket_row
	var count C.int
	if code := C.pw_socket_rows(C.int(pid), &rows, &count); code != 0 {
		return nil, syscall.Errno(code)
	}
	defer C.free(unsafe.Pointer(rows))
	if !holder.Current() {
		return nil, syscall.ESRCH
	}
	result := make([]SocketObservation, 0, int(count))
	for _, row := range unsafe.Slice(rows, int(count)) {
		var local, remote netip.Addr
		if row.ipv4 != 0 {
			var a, b [4]byte
			copy(a[:], unsafe.Slice((*byte)(unsafe.Pointer(&row.local[0])), 4))
			copy(b[:], unsafe.Slice((*byte)(unsafe.Pointer(&row.remote[0])), 4))
			local, remote = netip.AddrFrom4(a), netip.AddrFrom4(b)
		} else {
			var a, b [16]byte
			copy(a[:], unsafe.Slice((*byte)(unsafe.Pointer(&row.local[0])), 16))
			copy(b[:], unsafe.Slice((*byte)(unsafe.Pointer(&row.remote[0])), 16))
			local, remote = netip.AddrFrom16(a), netip.AddrFrom16(b)
		}
		protocol := ProtocolTCP
		if row.udp != 0 {
			protocol = ProtocolUDP
		}
		// Keep opaque kernel pointers out of logs and later receipt projections.
		identity := sha256.Sum256([]byte(fmt.Sprintf("darwin-%s:%d:%d:%d", protocol, row.socket_handle, row.pcb_handle, row.generation)))
		result = append(result, SocketObservation{
			Protocol: protocol, Local: netip.AddrPortFrom(local, uint16(row.local_port)), Remote: netip.AddrPortFrom(remote, uint16(row.remote_port)),
			ResourceID: fmt.Sprintf("%s:%x", protocol, identity), Listening: row.listening != 0, IPv6Only: row.ipv6_only != 0, Holder: holder, handle: uint64(row.socket_handle),
		})
	}
	return result, nil
}
