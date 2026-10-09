package app

import (
	"fmt"

	"github.com/lycaon/lycaon/internal/confine"
)

// wireEgressBroker binds the host's single mediation front door. Its address is
// recorded, so a process an earlier run left running still reaches a listener
// that answers and the proxy variables a command reads never change.
func (b *serveBuilder) wireEgressBroker() error {
	if !confine.Available() {
		return nil
	}
	addrs, err := confine.StartEgressBroker(b.storage.Directory)
	if err != nil {
		return fmt.Errorf("egress broker: %w", err)
	}
	b.egressBrokerBound = true
	b.startup.logger.Info("egress broker", "http", addrs.HTTP, "socks", addrs.SOCKS)
	return nil
}

// wireRefusalWatch starts reading the kernel's sandbox refusal reports before
// the first confined action, so every action's record starts complete.
func (b *serveBuilder) wireRefusalWatch() error {
	if !confine.Available() {
		return nil
	}
	confine.StartRefusalWatch(b.storage.Directory)
	b.refusalWatchStarted = true
	return nil
}
