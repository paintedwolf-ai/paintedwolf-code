//go:build windows

package confine

import (
	"fmt"
	"slices"
)

// ReadResourceLimit reports ErrLimitUnsupported: Windows has no per-process
// resource limits.
func ReadResourceLimit(resource string) (ResourceLimit, error) {
	if !slices.Contains(limitResources(), resource) {
		return ResourceLimit{}, fmt.Errorf("unknown resource %q", resource)
	}
	return ResourceLimit{}, ErrLimitUnsupported
}

// declaredResourceLimits: Windows has no per-process resource limits, so every
// resource is unsupported.
func declaredResourceLimits() map[string]LimitCapability { return map[string]LimitCapability{} }
