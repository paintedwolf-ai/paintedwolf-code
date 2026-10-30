package confine

import (
	"context"
	"net/netip"
	"testing"
)

func TestOwnedLoopbackObservationRequiresNarrowedActionAdmission(t *testing.T) {
	endpoint := netip.MustParseAddrPort("127.0.0.1:8123")
	observed := 0
	registration := &ownedLoopbackRegistration{observe: func(context.Context, EgressCommand, netip.AddrPort) (string, bool) {
		observed++
		return "actual-resource", true
	}}
	broker := newTestBroker()
	broker.ownedLoopback = registration
	broker.tokens["ordinary"] = EgressCommand{SessionID: "session"}
	broker.tokens["conditional"] = EgressCommand{SessionID: "session", OwnedLoopbackPorts: []uint16{8123}}
	if _, ok := broker.ownedLoopbackFor(t.Context(), "ordinary", endpoint); ok || observed != 0 {
		t.Fatal("listener observation supplied missing action admission")
	}
	if id, ok := broker.ownedLoopbackFor(t.Context(), "conditional", endpoint); !ok || id != "actual-resource" || observed != 1 {
		t.Fatal("narrow conditional observation was not checked")
	}
	if _, ok := broker.ownedLoopbackFor(t.Context(), "conditional", netip.MustParseAddrPort("127.0.0.1:8124")); ok || observed != 1 {
		t.Fatal("conditional admission widened ports")
	}
	delete(broker.tokens, "conditional")
	if _, ok := broker.ownedLoopbackFor(t.Context(), "conditional", endpoint); ok {
		t.Fatal("released action retained authority")
	}
}
