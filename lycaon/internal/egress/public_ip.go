// Package egress holds public-IP SSRF helpers shared by outbound catalog fetches.
package egress

import (
	"context"
	"fmt"
	"net/netip"
)

// DestinationDeniedError reports a destination policy refusal.
type DestinationDeniedError struct {
	Reason string
	Cause  error
}

func (e *DestinationDeniedError) Error() string {
	if e == nil {
		return "destination blocked"
	}
	return e.Reason
}

func (e *DestinationDeniedError) Unwrap() error { return e.Cause }

// NAT64 and 6to4 addresses are checked against the embedded IPv4 destination.
var (
	nat64Prefix     = netip.MustParsePrefix("64:ff9b::/96")
	sixToFourPrefix = netip.MustParsePrefix("2002::/16")
	teredoPrefix    = netip.MustParsePrefix("2001::/32")
	siteLocalPrefix = netip.MustParsePrefix("fec0::/10")
	discardPrefix   = netip.MustParsePrefix("100::/64")
	// netip classifies these special-use ranges as global unicast.
	nonPublic = []netip.Prefix{
		netip.MustParsePrefix("0.0.0.0/8"),
		netip.MustParsePrefix("100.64.0.0/10"),
		netip.MustParsePrefix("192.0.0.0/24"),
		netip.MustParsePrefix("192.0.2.0/24"),
		netip.MustParsePrefix("192.88.99.0/24"),
		netip.MustParsePrefix("198.18.0.0/15"),
		netip.MustParsePrefix("198.51.100.0/24"),
		netip.MustParsePrefix("203.0.113.0/24"),
		netip.MustParsePrefix("240.0.0.0/4"),
		netip.MustParsePrefix("64:ff9b:1::/48"),
		netip.MustParsePrefix("2001:2::/48"),
		netip.MustParsePrefix("2001:10::/28"),
		netip.MustParsePrefix("2001:20::/28"),
		netip.MustParsePrefix("2001:db8::/32"),
		netip.MustParsePrefix("3fff::/20"),
		netip.MustParsePrefix("5f00::/16"),
	}
)

func v4Embedded(addr netip.Addr, off int) netip.Addr {
	b := addr.As16()
	return netip.AddrFrom4([4]byte{b[off], b[off+1], b[off+2], b[off+3]})
}

// IPPublic reports whether addr is an allowed public destination.
func IPPublic(addr netip.Addr) bool {
	addr = addr.Unmap()
	if !addr.IsValid() {
		return false
	}
	if !addr.IsGlobalUnicast() || addr.IsLoopback() || addr.IsPrivate() || addr.IsUnspecified() ||
		addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() ||
		addr.IsMulticast() || addr.IsInterfaceLocalMulticast() {
		return false
	}
	for _, prefix := range nonPublic {
		if prefix.Contains(addr) {
			return false
		}
	}
	if addr.Is4() {
		return true
	}
	switch {
	case nat64Prefix.Contains(addr):
		return IPPublic(v4Embedded(addr, 12))
	case sixToFourPrefix.Contains(addr):
		return IPPublic(v4Embedded(addr, 2))
	case teredoPrefix.Contains(addr), siteLocalPrefix.Contains(addr), discardPrefix.Contains(addr):
		return false
	}
	return true
}

// ResolvePublicIPs resolves host and errors unless every address is public.
func ResolvePublicIPs(ctx context.Context, host string) ([]netip.Addr, error) {
	return ResolveIPsWithPolicy(ctx, host, IPPublic)
}

// ResolveIPsWithPolicy rejects the host if any resolved address is disallowed.
func ResolveIPsWithPolicy(ctx context.Context, host string, allow func(netip.Addr) bool) ([]netip.Addr, error) {
	return resolveIPsWithPolicy(ctx, host, allow, currentLookup())
}

func resolveIPsWithPolicy(ctx context.Context, host string, allow func(netip.Addr) bool, lookup func(context.Context, string, string) ([]netip.Addr, error)) ([]netip.Addr, error) {
	if allow == nil {
		allow = IPPublic
	}
	if lit, err := netip.ParseAddr(host); err == nil {
		if !allow(lit) {
			return nil, &DestinationDeniedError{Reason: fmt.Sprintf("blocked: %s is not an allowed address", host)}
		}
		return []netip.Addr{lit}, nil
	}
	ips, err := lookup(ctx, "ip", host)
	if err != nil {
		return nil, &DestinationDeniedError{Reason: fmt.Sprintf("blocked: dns resolution failed: %v", err), Cause: err}
	}
	if len(ips) == 0 {
		return nil, &DestinationDeniedError{Reason: fmt.Sprintf("blocked: no addresses for %s", host)}
	}
	for _, a := range ips {
		if !allow(a) {
			return nil, &DestinationDeniedError{Reason: fmt.Sprintf("blocked: %s resolves to a disallowed address", host)}
		}
	}
	return ips, nil
}
