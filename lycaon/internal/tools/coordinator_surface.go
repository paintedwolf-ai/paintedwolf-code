package tools

// Coordinator surfaces. Per-turn tool exposure and invoke authorization both
// derive from each surface's floor and loadable sets in coordinator-surfaces.yaml.
const (
	SurfaceImplementInvestigate = "implement_investigate"
	SurfaceImplementDispatch    = "implement_dispatch"
	// SurfaceAwaitHost serves the person's turns while the host holds the phase.
	SurfaceAwaitHost             = "await_host"
	CoordinatorProductWriteScope = "coordinator_product_write"
)
