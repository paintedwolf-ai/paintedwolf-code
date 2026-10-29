//go:build !windows

package confine

import (
	"fmt"
	"math"
	"os/signal"
	"syscall"

	"golang.org/x/sys/unix"
)

var limitResourceIDs = map[string]int{
	"core": unix.RLIMIT_CORE, "fsize": unix.RLIMIT_FSIZE, "nofile": unix.RLIMIT_NOFILE, "stack": unix.RLIMIT_STACK,
	"cpu": unix.RLIMIT_CPU, "nproc": unix.RLIMIT_NPROC, "as": unix.RLIMIT_AS,
}

// declaredResourceLimits: macOS, Linux and the other Unix platforms query
// each resource and start a launched program with a raised or lowered soft
// limit or a lowered hard one.
func declaredResourceLimits() map[string]LimitCapability {
	full := LimitQuery | LimitSetSoft | LimitSetHard
	return map[string]LimitCapability{"core": full, "fsize": full, "nofile": full, "stack": full, "cpu": full, "nproc": full, "as": full}
}

// ReadResourceLimit reports this process's current limit for resource.
func ReadResourceLimit(resource string) (ResourceLimit, error) {
	id, ok := limitResourceIDs[resource]
	if !ok {
		return ResourceLimit{}, fmt.Errorf("unknown resource %q", resource)
	}
	if ResourceLimitCapability(resource)&LimitQuery == 0 {
		return ResourceLimit{}, ErrLimitUnsupported
	}
	var raw syscall.Rlimit
	if err := syscall.Getrlimit(id, &raw); err != nil {
		return ResourceLimit{}, err
	}
	return ResourceLimit{Resource: resource, Soft: fromPlatformLimit(raw.Cur), Hard: fromPlatformLimit(raw.Max)}, nil
}

// applyProcessAttributes runs in the helper just before it becomes the
// target. syscall.Setrlimit also tells the Go runtime not to restore its
// own descriptor limit across exec, so the target starts with exactly these.
func applyProcessAttributes(a ProcessAttributes) error {
	for _, limit := range a.Limits {
		raw := syscall.Rlimit{Cur: toPlatformLimit(limit.Soft), Max: toPlatformLimit(limit.Hard)}
		if err := syscall.Setrlimit(limitResourceIDs[limit.Resource], &raw); err != nil {
			return fmt.Errorf("set %s limit: %w", limit.Resource, err)
		}
	}
	if a.IgnoreHangup {
		// An ignored disposition survives exec.
		signal.Ignore(syscall.SIGHUP)
	}
	return nil
}

func fromPlatformLimit(value uint64) uint64 {
	if value == unix.RLIM_INFINITY {
		return math.MaxUint64
	}
	return value
}

func toPlatformLimit(value uint64) uint64 {
	if value == math.MaxUint64 {
		return unix.RLIM_INFINITY
	}
	return value
}
