package lineage

import (
	"net/netip"
	"reflect"
	"syscall"
	"testing"
)

func TestReconcileListenersDecidesWhetherAbsenceIsAFact(t *testing.T) {
	const self, other, unreadable = 10, 11, 12
	ownPort := netip.MustParseAddrPort("127.0.0.1:3000")
	otherPort := netip.MustParseAddrPort("127.0.0.1:4000")
	ownRow := kernelListener{Protocol: ProtocolTCP, Local: ownPort, UID: 501, Handle: 1}
	otherRow := kernelListener{Protocol: ProtocolTCP, Local: otherPort, UID: 501, Handle: 2}
	rootRow := kernelListener{Protocol: ProtocolTCP, Local: netip.MustParseAddrPort("127.0.0.1:5000"), Handle: 3}
	ownListener := SocketObservation{Protocol: ProtocolTCP, Local: ownPort, Listening: true, Holder: fixtureIncarnation(self), handle: 1, uid: 501}
	otherListener := SocketObservation{Protocol: ProtocolTCP, Local: otherPort, Listening: true, Holder: fixtureIncarnation(other), handle: 2, uid: 501}
	otherConnection := SocketObservation{Protocol: ProtocolTCP, Holder: fixtureIncarnation(other), handle: 4}

	records := func(handles ...uint64) map[uint64]bool {
		out := map[uint64]bool{}
		for _, handle := range handles {
			out[handle] = true
		}
		return out
	}
	ownHeld := Listener{Protocol: ProtocolTCP, Addr: ownPort, UID: 501, Holders: []ProcessIdentity{fixtureIncarnation(self)}}
	otherHeld := Listener{Protocol: ProtocolTCP, Addr: otherPort, UID: 501, Holders: []ProcessIdentity{fixtureIncarnation(other)}}
	rootHeld := Listener{Protocol: ProtocolTCP, Addr: rootRow.Local}

	for _, tc := range []struct {
		name     string
		read     socketTableRead
		walked   map[int][]SocketObservation
		want     []Listener
		complete bool
	}{
		{
			name:     "every declared row delivered",
			read:     socketTableRead{Listening: []kernelListener{ownRow, otherRow, rootRow}, Records: records(1, 2, 3), Declared: 3},
			walked:   map[int][]SocketObservation{self: {ownListener}, other: {otherListener}},
			want:     []Listener{ownHeld, otherHeld, rootHeld},
			complete: true,
		},
		{
			name:     "closed sockets awaiting collection are not withholding",
			read:     socketTableRead{Listening: []kernelListener{ownRow, rootRow}, Records: records(1, 3, 4), Declared: 5},
			walked:   map[int][]SocketObservation{self: {ownListener}, other: {otherConnection}},
			want:     []Listener{ownHeld, rootHeld},
			complete: true,
		},
		{
			name:   "only this process's rows delivered",
			read:   socketTableRead{Listening: []kernelListener{ownRow}, Records: records(1), Declared: 23},
			walked: map[int][]SocketObservation{self: {ownListener}, other: {otherListener, otherConnection}},
			want:   []Listener{ownHeld, otherHeld},
		},
		{
			name:   "nothing delivered and nothing readable",
			read:   socketTableRead{Records: records(), Declared: 23},
			walked: map[int][]SocketObservation{},
		},
		{
			name:   "a listener only the walk holds",
			read:   socketTableRead{Listening: []kernelListener{rootRow}, Records: records(3, 4), Declared: 2},
			walked: map[int][]SocketObservation{other: {otherListener, otherConnection}},
			want:   []Listener{rootHeld, otherHeld},
		},
		{
			name:     "a truly empty table",
			read:     socketTableRead{Records: records()},
			walked:   map[int][]SocketObservation{},
			complete: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.read.Protocol = ProtocolTCP
			got := reconcileListeners([]socketTableRead{tc.read}, ListenerQuery{}, self, []int{0, self, other, unreadable}, func(pid int) ([]SocketObservation, error) {
				if pid <= 0 {
					t.Fatalf("walked invalid pid %d", pid)
				}
				if pid == unreadable {
					return nil, syscall.EPERM
				}
				return tc.walked[pid], nil
			})
			if !reflect.DeepEqual(got.Listeners, append([]Listener{}, tc.want...)) || got.Complete != tc.complete {
				t.Fatalf("got %+v, want listeners %+v complete=%v", got, tc.want, tc.complete)
			}
		})
	}
}

// A narrowed query does not hide withholding: the port it names may be the one
// the kernel kept back, and a walk-only listener outside the query stays out.
func TestReconcileListenersHonorsTheQueryWhenWithheld(t *testing.T) {
	other := SocketObservation{Protocol: ProtocolTCP, Local: netip.MustParseAddrPort("127.0.0.1:4000"), Listening: true, Holder: fixtureIncarnation(11), handle: 2}
	read := socketTableRead{Protocol: ProtocolTCP, Records: map[uint64]bool{}, Declared: 9}
	observe := func(int) ([]SocketObservation, error) { return []SocketObservation{other}, nil }
	got := reconcileListeners([]socketTableRead{read}, ListenerQuery{Port: 4001}, 10, []int{11}, observe)
	if len(got.Listeners) != 0 || got.Complete {
		t.Fatalf("query for an unseen port = %+v", got)
	}
	got = reconcileListeners([]socketTableRead{read}, ListenerQuery{Port: 4000}, 10, []int{11}, observe)
	if len(got.Listeners) != 1 || got.Listeners[0].Addr != other.Local || got.Complete {
		t.Fatalf("query for a walked port = %+v", got)
	}
	udp := socketTableRead{Protocol: ProtocolUDP, Records: map[uint64]bool{}}
	got = reconcileListeners([]socketTableRead{udp}, ListenerQuery{Protocols: []Protocol{ProtocolUDP}}, 10, []int{11}, observe)
	if len(got.Listeners) != 0 || !got.Complete {
		t.Fatalf("a tcp socket answered a udp query: %+v", got)
	}
}
