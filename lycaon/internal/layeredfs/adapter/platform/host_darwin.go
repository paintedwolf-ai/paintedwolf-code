//go:build darwin

package platform

import (
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter/nfsdarwin"
)

func newHostSelector(Config) *adapter.Selector {
	return adapter.NewSelector("darwin", nil, nfsdarwin.New(nfsdarwin.Config{}), adapter.NewEagerCopyAdapter(adapter.ReasonNone))
}

// ConnectionRecoveries names the adapters able to settle connections left by an earlier engine
// process. A mounted adapter absent here settles only once its mount is gone.
func ConnectionRecoveries() map[adapter.AdapterID]adapter.ConnectionRecovery {
	return map[adapter.AdapterID]adapter.ConnectionRecovery{adapter.AdapterNFSDarwin: nfsdarwin.New(nfsdarwin.Config{})}
}
