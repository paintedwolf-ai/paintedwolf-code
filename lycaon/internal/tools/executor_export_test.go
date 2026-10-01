package tools

import (
	"context"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/egressproxy"
)

// ResolveEgressForTest provides test-only access to the pre-dial gate.
func (e *DefaultToolExecutor) ResolveEgressForTest(ctx context.Context, cmd confine.EgressCommand, host string) bool {
	ep, err := egressproxy.ParseHTTPEndpoint(host, egressproxy.TransportHTTPConnect)
	if err != nil {
		ep = egressproxy.Endpoint{Host: host, Transport: egressproxy.TransportHTTPConnect, Port: 443}
	}
	return e.resolveEgress(ctx, cmd, ep, nil)
}
