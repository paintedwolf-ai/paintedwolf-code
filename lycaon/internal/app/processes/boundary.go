package processes

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/internal/confine"
)

func (b *Runtime) StartEgress(dataDir string) error {
	if !confine.Available() {
		return nil
	}
	addrs, err := confine.StartEgressBroker(dataDir)
	if err != nil {
		return fmt.Errorf("egress broker: %w", err)
	}
	b.resources.Track("egress-broker", 55, func(context.Context) error { return confine.StopEgressBroker() })
	b.logger.Info("egress broker", "http", addrs.HTTP, "socks", addrs.SOCKS)
	return nil
}

func (b *Runtime) StartRefusalWatch(ctx context.Context, dataDir string) error {
	if !confine.Available() {
		return nil
	}
	confine.StartRefusalWatch(ctx, dataDir)
	b.resources.Track("refusal-watch", 55, func(context.Context) error { confine.StopRefusalWatch(); return nil })
	return nil
}
