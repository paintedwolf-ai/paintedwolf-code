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
			}, nil)
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
	got := reconcileListeners([]socketTableRead{read}, ListenerQuery{Port: 4001}, 10, []int{11}, observe, nil)
	if len(got.Listeners) != 0 || got.Complete {
		t.Fatalf("query for an unseen port = %+v", got)
	}
	got = reconcileListeners([]socketTableRead{read}, ListenerQuery{Port: 4000}, 10, []int{11}, observe, nil)
	if len(got.Listeners) != 1 || got.Listeners[0].Addr != other.Local || got.Complete {
		t.Fatalf("query for a walked port = %+v", got)
	}
	udp := socketTableRead{Protocol: ProtocolUDP, Records: map[uint64]bool{}}
	got = reconcileListeners([]socketTableRead{udp}, ListenerQuery{Protocols: []Protocol{ProtocolUDP}}, 10, []int{11}, observe, nil)
	if len(got.Listeners) != 0 || !got.Complete {
		t.Fatalf("a tcp socket answered a udp query: %+v", got)
	}
}

// A listener the walk holds but the table read lacks can postdate the read:
// the socket was delivered in another state, appeared while the walk ran, or
// closed again before the holders were re-observed. Only a listener a holder
// still observes while both reads lack it is withholding.
func TestReconcileListenersDatesWalkOnlyListenersBeforeWithholding(t *testing.T) {
	self, other := 10, 11
	ownRow := kernelListener{Protocol: ProtocolTCP, Local: netip.MustParseAddrPort("127.0.0.1:3000"), UID: 501, Handle: 1}
	churned := SocketObservation{Protocol: ProtocolTCP, Local: netip.MustParseAddrPort("127.0.0.1:4000"), Listening: true, Holder: fixtureIncarnation(other), handle: 2, uid: 501}
	records := func(handles ...uint64) map[uint64]bool {
		out := map[uint64]bool{}
		for _, handle := range handles {
			out[handle] = true
		}
		return out
	}
	for _, tc := range []struct {
		name         string
		read         socketTableRead
		reread       socketTableRead
		reobserve    []SocketObservation
		reobserveErr error
		rereadCalls  int
		complete     bool
	}{
		{
			name:        "a handle the read delivered in another state",
			read:        socketTableRead{Protocol: ProtocolTCP, Listening: []kernelListener{ownRow}, Records: records(1, 2)},
			rereadCalls: 0,
			complete:    true,
		},
		{
			name:        "a fresh read dates the listener",
			read:        socketTableRead{Protocol: ProtocolTCP, Listening: []kernelListener{ownRow}, Records: records(1)},
			reread:      socketTableRead{Records: records(1, 2)},
			rereadCalls: 1,
			complete:    true,
		},
		{
			name:        "a listener closed before the re-observation",
			read:        socketTableRead{Protocol: ProtocolTCP, Listening: []kernelListener{ownRow}, Records: records(1)},
			reread:      socketTableRead{Records: records(1)},
			rereadCalls: 1,
			complete:    true,
		},
		{
			name:         "a holder exited before the re-observation",
			read:         socketTableRead{Protocol: ProtocolTCP, Listening: []kernelListener{ownRow}, Records: records(1)},
			reread:       socketTableRead{Records: records(1)},
			reobserveErr: syscall.ESRCH,
			rereadCalls:  1,
			complete:     true,
		},
		{
			name:        "a listener a holder still observes is withheld",
			read:        socketTableRead{Protocol: ProtocolTCP, Listening: []kernelListener{ownRow}, Records: records(1)},
			reread:      socketTableRead{Records: records(1)},
			reobserve:   []SocketObservation{churned},
			rereadCalls: 1,
		},
		{
			name:         "an unreadable holder keeps withholding",
			read:         socketTableRead{Protocol: ProtocolTCP, Listening: []kernelListener{ownRow}, Records: records(1)},
			reread:       socketTableRead{Records: records(1)},
			reobserveErr: syscall.EPERM,
			rereadCalls:  1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rereads := 0
			observed := 0
			got := reconcileListeners([]socketTableRead{tc.read}, ListenerQuery{}, self, []int{other}, func(int) ([]SocketObservation, error) {
				observed++
				if observed == 1 {
					return []SocketObservation{churned}, nil
				}
				return tc.reobserve, tc.reobserveErr
			}, func(Protocol) (socketTableRead, error) {
				rereads++
				return tc.reread, nil
			})
			if rereads != tc.rereadCalls {
				t.Fatalf("reread the table %d times, want %d", rereads, tc.rereadCalls)
			}
			if got.Complete != tc.complete {
				t.Fatalf("complete=%v, want %v", got.Complete, tc.complete)
			}
		})
	}
}
