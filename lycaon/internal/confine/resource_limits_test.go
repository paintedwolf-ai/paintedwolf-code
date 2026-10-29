package confine_test

import (
	"errors"
	"runtime"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
)

// Each platform's declared resource-limit set is pinned here; any platform
// not listed declares every resource unsupported.
func TestPlatformDeclaresItsResourceLimits(t *testing.T) {
	resources := []string{"core", "fsize", "nofile", "stack", "cpu", "nproc", "as"}
	full := confine.LimitQuery | confine.LimitSetSoft | confine.LimitSetHard
	declared := map[string]confine.LimitCapability{"darwin": full, "linux": full, "windows": confine.LimitUnsupported}
	want := declared[runtime.GOOS]
	for _, resource := range resources {
		capability := confine.ResourceLimitCapability(resource)
		if capability != want {
			t.Errorf("%s %s = %b, want %b", runtime.GOOS, resource, capability, want)
		}
		limit, err := confine.ReadResourceLimit(resource)
		if capability&confine.LimitQuery == 0 {
			if !errors.Is(err, confine.ErrLimitUnsupported) {
				t.Errorf("unsupported %s read: %+v, %v", resource, limit, err)
			}
			continue
		}
		if err != nil || limit.Resource != resource || limit.Soft > limit.Hard {
			t.Errorf("%s: %+v, %v", resource, limit, err)
		}
	}
	if confine.ResourceLimitCapability("rss") != confine.LimitUnsupported {
		t.Fatal("an undeclared resource has a capability")
	}
	if _, err := confine.ReadResourceLimit("rss"); err == nil || errors.Is(err, confine.ErrLimitUnsupported) {
		t.Fatalf("unknown resource read: %v", err)
	}
}
