package egressproxy

import (
	"context"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/hostproxy"
	"net"
	"net/netip"
	"net/url"
	"time"

	"github.com/lycaon/lycaon/internal/egress"
)

// DialEndpoint dials an authorized destination. Nil uses the address-pinned dialer.
type DialEndpoint func(ctx context.Context, ep Endpoint) (net.Conn, error)

var errCommandAuthorizationRevoked = errors.New("command proxy authorization was revoked")

// ErrLoopbackNotAuthorized reports missing authority for a resolved local port.
var ErrLoopbackNotAuthorized = errors.New("loopback connect is not authorized")

// loopbackPolicy checks resolved addresses, including DNS rebinding to loopback.
type loopbackPolicy struct {
	authorized bool
	// refused distinguishes a local authorization denial from a resolution failure.
	refused bool
}

func (p *loopbackPolicy) allow(addr netip.Addr) bool {
	if addr.Unmap().IsLoopback() {
		if !p.authorized {
			p.refused = true
		}
		return p.authorized
	}
	return egress.IPPublic(addr)
}

func defaultDialEndpoint(ctx context.Context, ep Endpoint, authorized func() bool, loopbackAuthorized bool) (net.Conn, error) {
	dialCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	policy := &loopbackPolicy{authorized: loopbackAuthorized}
	ips, err := egress.ResolveIPsWithPolicy(dialCtx, ep.Host, policy.allow)
	if err != nil {
		if policy.refused {
			return nil, fmt.Errorf("%w: connect to %s", ErrLoopbackNotAuthorized, ep.DialAddr())
		}
		return nil, err
	}
	scheme := "https"
	if ep.Transport == TransportHTTPRequest {
		scheme = "http"
	}
	// Opaque CONNECT/SOCKS streams use the HTTPS tunneling policy, with the
	// original host/port retained for exceptions and the endpoint's lease.
	target := &url.URL{Scheme: scheme, Host: ep.DialAddr(), Path: ep.RequestPath}
	var lastErr error
	for _, ip := range ips {
		if authorized != nil && !authorized() {
			return nil, errCommandAuthorizationRevoked
		}
		conn, dialErr := hostproxy.DialPinned(dialCtx, target, "tcp", netip.AddrPortFrom(ip, ep.Port))
		if dialErr == nil {
			if authorized != nil && !authorized() {
				_ = conn.Close()
				return nil, errCommandAuthorizationRevoked
			}
			return conn, nil
		}
		lastErr = dialErr
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("dial failed")
}
