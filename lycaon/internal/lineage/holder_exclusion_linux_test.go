//go:build linux

package lineage

import (
	"net/netip"
	"os"
	"syscall"
	"testing"
)

func TestTableExcludesOthersRequiresEveryReachableListenerAttributed(t *testing.T) {
	endpoint := netip.MustParseAddrPort("127.0.0.1:8123")
	row := func(local string, inode uint64, listening bool) procSocket {
		return procSocket{Protocol: ProtocolTCP, Local: netip.MustParseAddrPort(local), Listening: listening, Inode: inode}
	}
	walk := endpointHolders{holders: []SocketObservation{{handle: 1}}, sibling: []SocketObservation{{handle: 2}}}
	for _, tc := range []struct {
		name  string
		table []procSocket
		want  bool
	}{
		{name: "only the session's listeners", table: []procSocket{row("127.0.0.1:8123", 1, true), row("[::1]:8123", 2, true)}, want: true},
		{name: "other ports and connections", table: []procSocket{row("127.0.0.1:8123", 1, true), row("127.0.0.1:9000", 3, true), row("127.0.0.1:8123", 4, false)}, want: true},
		{name: "a second socket on the endpoint", table: []procSocket{row("127.0.0.1:8123", 1, true), row("127.0.0.1:8123", 3, true)}},
		{name: "another holder on the sibling", table: []procSocket{row("127.0.0.1:8123", 1, true), row("[::1]:8123", 3, true)}},
		{name: "an ipv4 wildcard", table: []procSocket{row("127.0.0.1:8123", 1, true), row("0.0.0.0:8123", 3, true)}},
		{name: "an ipv6 wildcard", table: []procSocket{row("127.0.0.1:8123", 1, true), row("[::]:8123", 3, true)}},
		{name: "a mapped wildcard", table: []procSocket{row("127.0.0.1:8123", 1, true), row("[::ffff:0.0.0.0]:8123", 3, true)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tableExcludesOthers(tc.table, endpoint, walk); got != tc.want {
				t.Fatalf("excluded = %v", got)
			}
		})
	}
}

func TestUnreadableByThisUserIsThePermissionRefusal(t *testing.T) {
	if !unreadableByThisUser(1, &os.PathError{Op: "open", Path: "/proc/1/fd", Err: syscall.EACCES}) {
		t.Error("a refused descriptor table was not treated as unreadable")
	}
	if unreadableByThisUser(1, &os.PathError{Op: "open", Path: "/proc/1/fd", Err: syscall.EIO}) {
		t.Error("an I/O failure was treated as a refusal")
	}
}
