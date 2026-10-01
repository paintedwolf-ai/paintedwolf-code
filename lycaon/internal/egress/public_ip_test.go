package egress_test

import (
	"net/netip"
	"testing"

	"github.com/lycaon/lycaon/internal/egress"
)

func TestIPPublic(t *testing.T) {
	tests := []struct {
		address string
		public  bool
	}{
		{"8.8.8.8", true},
		{"2606:4700:4700::1111", true},
		{"0.1.2.3", false},
		{"10.0.0.1", false},
		{"100.64.0.1", false},
		{"127.0.0.1", false},
		{"169.254.169.254", false},
		{"192.0.2.1", false},
		{"198.18.0.1", false},
		{"198.51.100.1", false},
		{"203.0.113.1", false},
		{"255.255.255.255", false},
		{"::1", false},
		{"fc00::1", false},
		{"fe80::1", false},
		{"2001:db8::1", false},
		{"3fff::1", false},
		{"64:ff9b::808:808", true},
		{"64:ff9b::a00:1", false},
		{"64:ff9b:1::a00:1", false},
		{"2002:0808:0808::1", true},
		{"2002:0a00:0001::1", false},
	}
	for _, tc := range tests {
		t.Run(tc.address, func(t *testing.T) {
			if got := egress.IPPublic(netip.MustParseAddr(tc.address)); got != tc.public {
				t.Fatalf("IPPublic(%s)=%t want %t", tc.address, got, tc.public)
			}
		})
	}
}
