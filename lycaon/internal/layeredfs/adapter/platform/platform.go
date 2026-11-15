// Package platform constructs the workspace adapter for the host this binary was built for. Build
// tags choose the adapter, so consumers name neither a platform package nor an operating system.
package platform

import "github.com/lycaon/lycaon/internal/layeredfs/adapter"

// Config supplies the host services a mounted adapter depends on.
type Config struct {
	// Confinement enforces direct-storage denial for mounted views. Without it the selector falls
	// back with adapter.ReasonConfinementUnavailable, and no mounted view is ever claimed ready.
	Confinement adapter.ConfinementEnforcer
}

// NewSelector returns the selector for this host: its mounted adapter, then materialized fallback.
func NewSelector(cfg Config) *adapter.Selector {
	sel := newHostSelector(cfg)
	sel.RequireConfinement = true
	sel.Confinement = cfg.Confinement
	return sel
}
