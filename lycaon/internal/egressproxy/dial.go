package egressproxy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
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
	var lastErr error
	for _, ip := range ips {
		if authorized != nil && !authorized() {
			return nil, errCommandAuthorizationRevoked
		}
		addr := net.JoinHostPort(ip.String(), fmt.Sprintf("%d", ep.Port))
		var dialer net.Dialer
		conn, dialErr := dialer.DialContext(dialCtx, "tcp", addr)
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
