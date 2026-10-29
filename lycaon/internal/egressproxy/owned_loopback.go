package egressproxy

import (
	"context"
	"net"
	"net/netip"
	"sync"
)

// OwnedLoopbackAuthority observes an exact live listener for this lineage under
// current policy. The opaque identity must change when its resource or owner
// changes. A request, listen grant or cached port number is not an observation.
type OwnedLoopbackAuthority func(context.Context, string, netip.AddrPort) (string, bool)

// SetOwnedLoopbackAuthority wires live derived pairing independently of explicit
// loopback grants. The dialer rechecks it before exposing request payload bytes.
func (b *Broker) SetOwnedLoopbackAuthority(authority OwnedLoopbackAuthority) {
	if b != nil {
		b.ownedLoopback = authority
	}
}

// SetLoopbackRefusalReporter records an observed ownership refusal separately
// from failed dialing or unattributable callers. Earlier connection facts remain.
func (b *Broker) SetLoopbackRefusalReporter(report func(context.Context, string, Endpoint)) {
	if b != nil {
		b.reportLoopbackRefusal = report
	}
}

type endpointOwnership func(context.Context, netip.AddrPort) (string, bool)

type ownedLoopbackConn struct {
	net.Conn
	ctx        context.Context
	endpoint   netip.AddrPort
	resourceID string
	observe    endpointOwnership
	refused    func()
	once       sync.Once
}

func (c *ownedLoopbackConn) valid() bool {
	if c.ctx.Err() != nil {
		return false
	}
	id, ok := c.observe(c.ctx, c.endpoint)
	return ok && id != "" && id == c.resourceID
}

func (c *ownedLoopbackConn) Write(data []byte) (int, error) {
	if !c.valid() {
		c.once.Do(func() {
			_ = c.Conn.Close()
			if c.refused != nil {
				c.refused()
			}
		})
		return 0, ErrLoopbackNotAuthorized
	}
	return c.Conn.Write(data)
}
