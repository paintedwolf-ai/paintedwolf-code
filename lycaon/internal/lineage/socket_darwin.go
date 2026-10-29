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
	holder, ok := processIdentity(pid)
	if !ok {
		return nil, syscall.ESRCH
	}
	var rows *C.struct_pw_tcp_row
	var count C.int
	if code := C.pw_tcp_rows(C.int(pid), &rows, &count); code != 0 {
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
		// Keep opaque kernel pointers out of logs and later receipt projections.
		identity := sha256.Sum256([]byte(fmt.Sprintf("darwin-tcp:%d:%d:%d", row.socket_handle, row.pcb_handle, row.generation)))
		result = append(result, SocketObservation{
			Local: netip.AddrPortFrom(local, uint16(row.local_port)), Remote: netip.AddrPortFrom(remote, uint16(row.remote_port)),
			ResourceID: fmt.Sprintf("tcp:%x", identity), Listening: row.listening != 0, Holder: holder,
		})
	}
	return result, nil
}
