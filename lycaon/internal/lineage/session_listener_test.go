package lineage

import (
	"errors"
	"net/netip"
	"syscall"
	"testing"
)

func TestWalkEndpointHoldersDecidesWhatAnUnreadProcessLeavesOpen(t *testing.T) {
	const chat, other, denied, unexplained, gone = 10, 11, 12, 13, 14
	endpoint := netip.MustParseAddrPort("127.0.0.1:8123")
	listener := func(pid int, local string) SocketObservation {
		return SocketObservation{Local: netip.MustParseAddrPort(local), ResourceID: local, Listening: true, Holder: ProcessIdentity{PID: pid, Started: 1}}
	}
	chatListener := listener(chat, "127.0.0.1:8123")
	chatSibling := listener(chat, "[::1]:8123")
	owned := map[string]bool{socketIdentity(chatListener): true, socketIdentity(chatSibling): true}

	for _, tc := range []struct {
		name     string
		pids     []int
		sockets  map[int][]SocketObservation
		holders  int
		sibling  int
		foreign  bool
		unread   bool
		wantsErr bool
	}{
		{name: "every process read", pids: []int{0, chat}, sockets: map[int][]SocketObservation{chat: {chatListener}}, holders: 1},
		{name: "another user's process stays unknown", pids: []int{denied, chat}, sockets: map[int][]SocketObservation{chat: {chatListener}}, holders: 1, unread: true},
		{name: "an exited process is not unknown", pids: []int{gone, chat}, sockets: map[int][]SocketObservation{chat: {chatListener}}, holders: 1},
		{name: "any other read failure is incomplete", pids: []int{unexplained, chat}, sockets: map[int][]SocketObservation{chat: {chatListener}}, wantsErr: true},
		{name: "a readable foreign holder blocks", pids: []int{chat, other}, sockets: map[int][]SocketObservation{chat: {chatListener}, other: {listener(other, "127.0.0.1:8123")}}, holders: 1, foreign: true},
		{name: "a foreign wildcard blocks", pids: []int{other}, sockets: map[int][]SocketObservation{other: {listener(other, "0.0.0.0:8123")}}, foreign: true},
		{name: "an owned wildcard blocks", pids: []int{chat}, sockets: map[int][]SocketObservation{chat: {listener(chat, "[::]:8123")}}, foreign: true},
		{name: "the session's sibling is recorded", pids: []int{chat}, sockets: map[int][]SocketObservation{chat: {chatListener, chatSibling}}, holders: 1, sibling: 1},
		{name: "another's sibling is left to the exclusion", pids: []int{chat, other}, sockets: map[int][]SocketObservation{chat: {chatListener}, other: {listener(other, "[::1]:8123")}}, holders: 1},
		{name: "other ports and closed sockets do not count", pids: []int{other}, sockets: map[int][]SocketObservation{other: {listener(other, "127.0.0.1:9000"), {Local: endpoint}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			observe := func(pid int) ([]SocketObservation, error) {
				switch pid {
				case denied, unexplained:
					return nil, syscall.EPERM
				case gone:
					return nil, syscall.ESRCH
				}
				return tc.sockets[pid], nil
			}
			isDenied := func(pid int, err error) bool { return pid == denied && errors.Is(err, syscall.EPERM) }
			walk, err := walkEndpointHolders(t.Context(), endpoint, owned, tc.pids, observe, isDenied)
			if tc.wantsErr {
				if !errors.Is(err, ErrObservationIncomplete) {
					t.Fatalf("err = %v, want incomplete", err)
				}
				return
			}
			if err != nil || len(walk.holders) != tc.holders || len(walk.sibling) != tc.sibling || walk.foreign != tc.foreign || walk.unread != tc.unread {
				t.Fatalf("walk = %+v, err = %v", walk, err)
			}
		})
	}
}
