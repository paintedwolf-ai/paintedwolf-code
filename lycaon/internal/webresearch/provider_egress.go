package webresearch

import (
	"context"
	"fmt"
	"net/http"
	"net/netip"
	"net/url"
	"strings"

	"github.com/lycaon/lycaon/internal/egress"
	"github.com/lycaon/lycaon/internal/egressclass"
	"github.com/lycaon/lycaon/internal/egressgate"
	"github.com/lycaon/lycaon/internal/httpclient"
)

// providerEgressTestRelax permits loopback HTTP in provider tests.
var providerEgressTestRelax bool

// validateProviderEndpoint normalizes the URL and checks its address policy.
func validateProviderEndpoint(raw string, allowPrivate bool) (*url.URL, error) {
	u, err := normalizeFetchURL(raw)
	if err != nil {
		return nil, err
	}
	scheme := strings.ToLower(u.Scheme)
	if !allowPrivate && !providerEgressTestRelax && scheme != "https" {
		return nil, fmt.Errorf("provider endpoint must use https")
	}
	host := u.Hostname()
	if lit, err := netip.ParseAddr(host); err == nil {
		if !allowPrivate && !providerEgressTestRelax {
			return nil, fmt.Errorf("provider endpoint must use a hostname, not a bare IP")
		}
		if !ipProviderEgress(lit, allowPrivate) {
			return nil, fmt.Errorf("blocked: %s is not an allowed provider address", host)
		}
	}
	return u, nil
}

// ValidateProviderConfigEndpoint checks an endpoint before persistence.
func ValidateProviderConfigEndpoint(ctx context.Context, raw string, allowPrivate bool) error {
	u, err := validateProviderEndpoint(raw, allowPrivate)
	if err != nil {
		return err
	}
	_, err = resolveProviderEgressIPs(ctx, u.Hostname(), allowPrivate)
	return err
}

// ipProviderEgress admits private ranges without admitting local infrastructure.
func ipProviderEgress(addr netip.Addr, allowPrivate bool) bool {
	if providerEgressTestRelax {
		return ipAllowed(addr)
	}
	addr = addr.Unmap()
	if !addr.IsValid() {
		return false
	}
	if addr.IsLoopback() || addr.IsUnspecified() ||
		addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() ||
		addr.IsMulticast() || addr.IsInterfaceLocalMulticast() {
		return false
	}
	if allowPrivate && addr.IsPrivate() {
		return true
	}
	return egress.IPPublic(addr)
}

// resolveProviderEgressIPs resolves host under the provider egress policy.
func resolveProviderEgressIPs(ctx context.Context, host string, allowPrivate bool) ([]netip.Addr, error) {
	return resolveIPsWithPolicy(ctx, host, func(a netip.Addr) bool {
		return ipProviderEgress(a, allowPrivate)
	})
}

// providerEgressClient pins validated addresses and applies the host gate.
func providerEgressClient(ctx context.Context, u *url.URL, allowPrivate bool) (*http.Client, error) {
	if _, err := validateProviderEndpoint(u.String(), allowPrivate); err != nil {
		return nil, err
	}
	host := u.Hostname()
	ips, err := resolveProviderEgressIPs(ctx, host, allowPrivate)
	if err != nil {
		return nil, err
	}
	if err := egressgate.AwaitHost(ctx, host); err != nil {
		return nil, err
	}
	// The request context sets the deadline.
	return httpclient.WithTransport(
		httpclient.Streaming(egressclass.WebResearchRequest),
		egress.PinnedTransport(host, ips, 0),
	), nil
}
