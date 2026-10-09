package egress

import (
	"context"
	"net"
	"net/netip"
	"strings"
	"sync/atomic"
)

// Lookup resolves a host name the way net.Resolver.LookupNetIP does.
type Lookup func(ctx context.Context, network, host string) ([]netip.Addr, error)

var lookupOverride atomic.Pointer[Lookup]

func currentLookup() Lookup {
	if lookup := lookupOverride.Load(); lookup != nil {
		return *lookup
	}
	return net.DefaultResolver.LookupNetIP
}

// TestingResolve answers every egress policy lookup from lookup until t ends,
// so destination checks run without DNS.
func TestingResolve(t interface {
	Helper()
	Cleanup(func())
}, lookup Lookup) {
	t.Helper()
	previous := lookupOverride.Swap(&lookup)
	t.Cleanup(func() { lookupOverride.Store(previous) })
}

// reservedSuffixes are the special-use names (RFC 2606, RFC 6761) that never
// resolve on the public network.
var reservedSuffixes = []string{"localhost", "invalid", "test", "example", "local"}

// StaticLookup answers every public host name with addrs. Special-use names
// are not found, as on the real network.
func StaticLookup(addrs ...netip.Addr) Lookup {
	return func(_ context.Context, _ string, host string) ([]netip.Addr, error) {
		name := strings.ToLower(strings.TrimSuffix(host, "."))
		for _, suffix := range reservedSuffixes {
			if name == suffix || strings.HasSuffix(name, "."+suffix) {
				return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
			}
		}
		return append([]netip.Addr(nil), addrs...), nil
	}
}
