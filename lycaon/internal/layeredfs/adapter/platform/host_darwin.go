//go:build darwin

package platform

import (
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter/nfsdarwin"
)

func newHostSelector(Config) *adapter.Selector {
	return adapter.NewSelector("darwin", nil, nfsdarwin.New(nfsdarwin.Config{}), adapter.NewEagerCopyAdapter(adapter.ReasonNone))
}
