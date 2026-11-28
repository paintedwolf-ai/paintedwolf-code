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

// BrokerDecisionObserver receives attributed egress policy decisions taken by
// the egress broker. Installed by higher-level diagnostics (e.g.
// internal/egressgate) to avoid an import cycle from internal/confine into
// internal/observability/cmddiag.
type BrokerDecisionObserver func(cmd EgressCommand, ep egressproxy.Endpoint, allowed bool)

var (
	brokerConnObserver     atomic.Pointer[BrokerConnectionObserver]
	brokerDecisionObserver atomic.Pointer[BrokerDecisionObserver]
)

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

// SetBrokerDecisionObserver installs the process-wide broker policy decision
// observer and returns a restore function. Passing nil clears the observer.
func SetBrokerDecisionObserver(fn BrokerDecisionObserver) func() {
	var next *BrokerDecisionObserver
	if fn != nil {
		cp := fn
		next = &cp
	}
	prev := brokerDecisionObserver.Swap(next)
	return func() {
		brokerDecisionObserver.Store(prev)
	}
}

// observeBrokerDecisionWEA forwards an attributed egress broker policy decision
// to the registered BrokerDecisionObserver. Zero-allocation no-op when unset.
func observeBrokerDecisionWEA(cmd EgressCommand, ep egressproxy.Endpoint, allowed bool) bool {
	fn := brokerDecisionObserver.Load()
	if fn == nil || *fn == nil {
		return false
	}
	(*fn)(cmd, ep, allowed)
	return true
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
