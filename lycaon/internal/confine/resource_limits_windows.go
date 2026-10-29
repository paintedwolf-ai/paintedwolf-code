//go:build windows

package confine

// declaredResourceLimits: Windows has no per-process resource limits, so every
// resource is unsupported.
func declaredResourceLimits() map[string]LimitCapability { return map[string]LimitCapability{} }
