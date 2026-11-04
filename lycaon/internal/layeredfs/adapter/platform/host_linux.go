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

// ConnectionRecoveries names the adapters able to settle connections left by an earlier engine
// process. A mounted adapter absent here settles only once its mount is gone.
func ConnectionRecoveries() map[adapter.AdapterID]adapter.ConnectionRecovery {
	return map[adapter.AdapterID]adapter.ConnectionRecovery{adapter.AdapterFUSELinux: fuselinux.New(fuselinux.Config{})}
}
