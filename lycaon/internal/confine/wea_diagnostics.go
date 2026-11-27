package confine

import (
	"sync/atomic"

	"github.com/lycaon/lycaon/internal/egressproxy"
)

// BrokerConnectionObserver receives attributed upstream dial connection events
// from the egress broker. Installed by higher-level diagnostics (e.g.
// internal/egressgate) to avoid an import cycle from internal/confine into
// internal/observability/cmddiag.
type BrokerConnectionObserver func(cmd EgressCommand, ep egressproxy.Endpoint)

var brokerConnObserver atomic.Pointer[BrokerConnectionObserver]

// SetBrokerConnectionObserver installs the process-wide broker connection
// observer and returns a restore function. Passing nil clears the observer.
func SetBrokerConnectionObserver(fn BrokerConnectionObserver) func() {
	var next *BrokerConnectionObserver
	if fn != nil {
		cp := fn
		next = &cp
	}
	prev := brokerConnObserver.Swap(next)
	return func() {
		brokerConnObserver.Store(prev)
	}
}

// observeBrokerConnectionWEA forwards an attributed upstream connection event
// to the registered BrokerConnectionObserver. When no observer is installed,
// this is an O(1), zero-allocation no-op.
func observeBrokerConnectionWEA(cmd EgressCommand, ep egressproxy.Endpoint) bool {
	fn := brokerConnObserver.Load()
	if fn == nil || *fn == nil {
		return false
	}
	(*fn)(cmd, ep)
	return true
}
