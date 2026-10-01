package egressproxy

import (
	"net/netip"
	"testing"
)

// Dial policy uses the resolved address and local-connect grant.
func TestDialPolicyDecidesLoopbackFromResolvedAddressAndGrant(t *testing.T) {
	loopback := netip.MustParseAddr("127.0.0.1")
	loopback6 := netip.MustParseAddr("::1")
	public := netip.MustParseAddr("93.184.216.34")
	private := netip.MustParseAddr("10.0.0.5")
	metadata := netip.MustParseAddr("169.254.169.254")

	cases := []struct {
		name          string
		authorized    bool
		addr          netip.Addr
		wantPermitted bool
	}{
		{"loopback without a grant", false, loopback, false},
		{"ipv6 loopback without a grant", false, loopback6, false},
		{"loopback with a grant", true, loopback, true},
		{"ipv6 loopback with a grant", true, loopback6, true},
		{"public address needs no local grant", false, public, true},
		{"private space is never reached", true, private, false},
		{"cloud metadata is never reached", true, metadata, false},
	}
	for _, c := range cases {
		policy := &loopbackPolicy{authorized: c.authorized}
		if got := policy.allow(c.addr); got != c.wantPermitted {
			t.Errorf("%s: allow(%s) = %v, want %v", c.name, c.addr, got, c.wantPermitted)
		}
		if refused := policy.refused; refused != (c.addr.Unmap().IsLoopback() && !c.authorized) {
			t.Errorf("%s: refusal axis recorded = %v", c.name, refused)
		}
	}
}
