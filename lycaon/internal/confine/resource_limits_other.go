//go:build !darwin && !linux && !windows

package confine

// declaredResourceLimits: platforms without a qualified resource-limit
// adapter declare every resource unsupported.
func declaredResourceLimits() map[string]LimitCapability { return map[string]LimitCapability{} }
