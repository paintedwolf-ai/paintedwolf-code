//go:build !linux && !darwin

package platform

import "github.com/lycaon/lycaon/internal/layeredfs/adapter"

func newHostSelector(Config) *adapter.Selector {
	return adapter.NewSelector("", nil, nil, adapter.NewEagerCopyAdapter(adapter.ReasonNone))
}
