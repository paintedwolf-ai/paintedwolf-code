//go:build !windows

package confine

import (
	"context"
	"fmt"
	"math"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
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

// ChildResourceLimit reports the limit a program launched with none set
// receives for resource.
func ChildResourceLimit(ctx context.Context, resource string) (ResourceLimit, error) {
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
	limit := ResourceLimit{Resource: resource, Soft: fromPlatformLimit(raw.Cur), Hard: fromPlatformLimit(raw.Max)}
	if resource == "nofile" {
		soft, err := childDescriptorSoftLimit(ctx)
		if err != nil {
			return ResourceLimit{}, err
		}
		limit.Soft = soft
	}
	return limit, nil
}

// childDescriptorSoftLimit measures a started child. This process cannot read
// the value: the Go runtime raised its own soft limit at startup, and it keeps
// the original privately to restore in each process it starts.
func childDescriptorSoftLimit(ctx context.Context) (uint64, error) {
	command := exec.CommandContext(ctx, "/bin/sh", "-c", "ulimit -Sn")
	command.Env = []string{}
	command.Dir = "/"
	out, err := command.Output()
	if err != nil {
		return 0, fmt.Errorf("measure the child descriptor limit: %w", err)
	}
	return parseChildLimit(string(out))
}

func parseChildLimit(report string) (uint64, error) {
	report = strings.TrimSuffix(report, "\n")
	if report == "unlimited" {
		return math.MaxUint64, nil
	}
	value, err := strconv.ParseUint(report, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("measure the child descriptor limit: unexpected report %q", report)
	}
	return value, nil
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
