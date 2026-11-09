//go:build darwin

package platform

import (
	"context"

	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter/nfsdarwin"
)

func newHostSelector(cfg Config) *adapter.Selector {
	var nfsCfg nfsdarwin.Config
	if cfg.Confinement != nil {
		nfsCfg.ConfinementCheckFn = func(ctx context.Context, probeCfg adapter.ProbeConfig) error {
			return cfg.Confinement.Available(ctx, probeCfg)
		}
		nfsCfg.ConfinementVerifyFn = cfg.Confinement.VerifyView
	}
	return adapter.NewSelector("darwin", nil, nfsdarwin.New(nfsCfg), adapter.NewEagerCopyAdapter(adapter.ReasonNone))
}

// ConnectionRecoveries names the adapters able to settle connections left by an earlier engine
// process. A mounted adapter absent here settles only once its mount is gone.
func ConnectionRecoveries() map[adapter.AdapterID]adapter.ConnectionRecovery {
	return map[adapter.AdapterID]adapter.ConnectionRecovery{adapter.AdapterNFSDarwin: nfsdarwin.New(nfsdarwin.Config{})}
}
