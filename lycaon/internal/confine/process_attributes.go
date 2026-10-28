package confine

import (
	"fmt"
	"runtime"
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

// LimitResources names the resources a helper can set, by their ulimit
// option letter.
var LimitResources = map[byte]string{'c': "core", 'f': "fsize", 'n': "nofile", 's': "stack", 't': "cpu", 'u': "nproc", 'v': "as"}

// arguments renders the attributes as helper flags. Windows has no helper
// re-exec; attributes there come from the platform launch itself.
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
	known := false
	for _, name := range LimitResources {
		known = known || name == resource
	}
	if !ok || !pair || !known {
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
