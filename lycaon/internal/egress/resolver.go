package egress

import (
	"context"
	"net"
	"net/netip"
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

// reservedSuffixes are the special-use names (RFC 2606, RFC 6761) that never
// resolve on the public network.
var reservedSuffixes = []string{"localhost", "invalid", "test", "example", "local"}
