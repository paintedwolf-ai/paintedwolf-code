//go:build windows

package confine

import "math"

// ReadResourceLimit reports no per-process limit: Windows has no rlimits.
func ReadResourceLimit(resource string) (ResourceLimit, error) {
	return ResourceLimit{Resource: resource, Soft: math.MaxUint64, Hard: math.MaxUint64}, nil
}
