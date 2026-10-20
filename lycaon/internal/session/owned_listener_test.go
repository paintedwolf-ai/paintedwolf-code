package session

import (
	"context"
	"errors"
	"net/netip"
	"testing"
)

type exactListenerFixture struct {
	fixedProvenance
	id  string
	err error
}

func (f exactListenerFixture) ObserveSessionListener(context.Context, string, netip.AddrPort) (string, bool, error) {
	return f.id, true, f.err
}

type portOnlyFixture struct{ LoopbackProvenanceResolver }

func TestOwnedListenerRequiresExactCompleteObservation(t *testing.T) {
	endpoint := netip.MustParseAddrPort("127.0.0.1:8123")
	for _, fixture := range []LoopbackProvenanceResolver{nil, portOnlyFixture{}, exactListenerFixture{}, exactListenerFixture{id: "partial", err: errors.New("incomplete")}} {
		if _, ok := ObserveOwnedSessionListener(t.Context(), fixture, "session", endpoint); ok {
			t.Fatal("missing/incomplete/exclusively port-based evidence granted access")
		}
	}
	fixture := exactListenerFixture{id: "resource"}
	if id, ok := ObserveOwnedSessionListener(t.Context(), fixture, "session", endpoint); !ok || id != "resource" {
		t.Fatal("exact resource lost")
	}
	for _, value := range []string{"0.0.0.0:8123", "192.0.2.1:8123", "127.0.0.1:0"} {
		if _, ok := ObserveOwnedSessionListener(t.Context(), fixture, "session", netip.MustParseAddrPort(value)); ok {
			t.Fatal("nonexact loopback accepted")
		}
	}
}
