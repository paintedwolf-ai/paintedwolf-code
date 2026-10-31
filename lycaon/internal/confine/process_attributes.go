package confine

import (
	"errors"
	"fmt"
	"runtime"
	"slices"
	"strconv"
	"strings"
)

// ResourceLimit is one per-process limit a launched program starts with, in
// the resource's native unit (bytes, seconds, descriptors or processes).
// Unlimited is math.MaxUint64.
type ResourceLimit struct {
	Resource   string
	Soft, Hard uint64
}

// ProcessAttributes are what the helper sets in its own process immediately
// before it becomes the target, so the target and everything it starts
// inherit them: resource limits and an ignored SIGHUP.
type ProcessAttributes struct {
	Limits       []ResourceLimit
	IgnoreHangup bool
}

const (
	resourceLimitFlag = "--resource-limit"
	ignoreHangupFlag  = "--ignore-hangup"
)

// LimitCapability is what a platform provides for one per-process resource
// limit. The zero value is unsupported: the platform has no such limit.
type LimitCapability uint8

const (
	// LimitQuery reports the host process's current soft and hard limits.
	LimitQuery LimitCapability = 1 << iota
	// LimitSetSoft starts a launched program with a soft limit up to the hard one.
	LimitSetSoft
	// LimitSetHard starts a launched program with a lowered hard limit.
	LimitSetHard
)

// LimitUnsupported is the capability of a resource the platform does not limit.
const LimitUnsupported LimitCapability = 0

// ErrLimitUnsupported reports a resource limit the platform does not provide.
var ErrLimitUnsupported = errors.New("resource limit unsupported on this platform")

// ResourceLimitCapability reports this platform's declared capability for a
// resource: core, fsize, nofile, stack, cpu, nproc or as.
func ResourceLimitCapability(resource string) LimitCapability {
	return declaredResourceLimits()[resource]
}

// limitResources are the resources a helper can set.
func limitResources() []string {
	return []string{"core", "fsize", "nofile", "stack", "cpu", "nproc", "as"}
}

// arguments renders the attributes as helper flags. Windows launches without
// the helper and has neither resource limits nor SIGHUP.
func (a ProcessAttributes) arguments() []string {
	if runtime.GOOS == "windows" {
		return nil
	}
	var out []string
	for _, limit := range a.Limits {
		out = append(out, resourceLimitFlag, limit.Resource+"="+strconv.FormatUint(limit.Soft, 10)+":"+strconv.FormatUint(limit.Hard, 10))
	}
	if a.IgnoreHangup {
		out = append(out, ignoreHangupFlag)
	}
	return out
}

func parseResourceLimit(value string) (ResourceLimit, error) {
	resource, bounds, ok := strings.Cut(value, "=")
	soft, hard, pair := strings.Cut(bounds, ":")
	if !ok || !pair || !slices.Contains(limitResources(), resource) {
		return ResourceLimit{}, fmt.Errorf("%s requires RESOURCE=SOFT:HARD, got %q", resourceLimitFlag, value)
	}
	limit := ResourceLimit{Resource: resource}
	var err error
	if limit.Soft, err = strconv.ParseUint(soft, 10, 64); err == nil {
		limit.Hard, err = strconv.ParseUint(hard, 10, 64)
	}
	if err != nil || limit.Soft > limit.Hard {
		return ResourceLimit{}, fmt.Errorf("%s requires SOFT <= HARD, got %q", resourceLimitFlag, value)
	}
	return limit, nil
}
