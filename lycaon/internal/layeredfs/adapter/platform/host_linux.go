//go:build linux

package platform

import (
	"context"

	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter/fuselinux"
)

func newHostSelector(cfg Config) *adapter.Selector {
	var fuseCfg fuselinux.Config
	if cfg.Confinement != nil {
		fuseCfg.ConfinementCheckFn = func(ctx context.Context, probeCfg adapter.ProbeConfig) error {
			return cfg.Confinement.Available(ctx, probeCfg)
		}
	}
	return adapter.NewSelector("linux", fuselinux.New(fuseCfg), nil, adapter.NewEagerCopyAdapter(adapter.ReasonNone))
}
