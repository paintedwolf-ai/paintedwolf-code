package egress

import (
	"context"
	"errors"
	"net/netip"
	"reflect"
	"testing"
)

func TestResolverRequiresEveryAddressToBePublic(t *testing.T) {
	for _, tc := range []struct {
		name      string
		addresses []string
		allowed   bool
	}{
		{name: "public IPv4 and IPv6", addresses: []string{"8.8.8.8", "2606:4700:4700::1111"}, allowed: true},
		{name: "mixed loopback", addresses: []string{"8.8.8.8", "127.0.0.1"}},
		{name: "mapped loopback first", addresses: []string{"::ffff:127.0.0.1", "8.8.8.8"}},
		{name: "mixed metadata", addresses: []string{"8.8.8.8", "169.254.169.254"}},
		{name: "mixed private IPv6", addresses: []string{"8.8.8.8", "fc00::1"}},
		{name: "no addresses"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			addresses := tc.addresses
			ips := make([]netip.Addr, len(addresses))
			for i, raw := range addresses {
				ips[i] = netip.MustParseAddr(raw)
			}
			lookup := func(_ context.Context, network, host string) ([]netip.Addr, error) {
				if network != "ip" || host != "feed.example" {
					t.Fatalf("lookup %s/%s", network, host)
				}
				return ips, nil
			}
			got, err := resolveIPsWithPolicy(t.Context(), "feed.example", nil, lookup)
			if tc.allowed {
				if err != nil || !reflect.DeepEqual(got, ips) {
					t.Fatalf("resolved %v, %v", got, err)
				}
			} else {
				var denied *DestinationDeniedError
				if !errors.As(err, &denied) || got != nil {
					t.Fatalf("unsafe result %v, %v", got, err)
				}
			}
		})
	}
}

func TestResolverRetainsFailureCause(t *testing.T) {
	for _, failure := range []error{errors.New("resolver unavailable"), context.DeadlineExceeded, context.Canceled} {
		lookup := func(context.Context, string, string) ([]netip.Addr, error) { return nil, failure }
		_, err := resolveIPsWithPolicy(t.Context(), "feed.example", IPPublic, lookup)
		var denied *DestinationDeniedError
		if !errors.As(err, &denied) || !errors.Is(err, failure) {
			t.Fatalf("lookup cause lost: %v", err)
		}
	}
}

func TestResolverLiteralsDoNotResolveAgain(t *testing.T) {
	lookup := func(context.Context, string, string) ([]netip.Addr, error) {
		t.Fatal("literal triggered DNS")
		return nil, nil
	}
	ips, err := resolveIPsWithPolicy(t.Context(), "8.8.8.8", nil, lookup)
	if err != nil || len(ips) != 1 || ips[0].String() != "8.8.8.8" {
		t.Fatalf("literal result %v, %v", ips, err)
	}
}
