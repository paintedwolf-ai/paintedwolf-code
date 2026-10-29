package command

import (
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/confine"
)

// The evaluator sees exactly the capabilities and limits confine declares.
func TestConfineLimitsServeTheDeclaredCapabilities(t *testing.T) {
	for _, resource := range []string{"core", "fsize", "nofile", "stack", "cpu", "nproc", "as", "rss"} {
		declared := confine.ResourceLimitCapability(resource)
		got := confineLimits{}.Capability(resource)
		for _, pair := range []struct {
			from confine.LimitCapability
			to   commandprogram.LimitCapability
		}{{confine.LimitQuery, commandprogram.LimitQuery}, {confine.LimitSetSoft, commandprogram.LimitSetSoft}, {confine.LimitSetHard, commandprogram.LimitSetHard}} {
			if (declared&pair.from != 0) != (got&pair.to != 0) {
				t.Fatalf("%s: confine %b, evaluator %b", resource, declared, got)
			}
		}
		limit, err := confineLimits{}.Read(resource)
		host, hostErr := confine.ReadResourceLimit(resource)
		if (err == nil) != (hostErr == nil) || limit != (commandprogram.ResourceLimit{Resource: host.Resource, Soft: host.Soft, Hard: host.Hard}) {
			t.Fatalf("%s read %+v (%v), confine %+v (%v)", resource, limit, err, host, hostErr)
		}
		if declared&confine.LimitQuery == 0 && resource != "rss" && !errors.Is(err, confine.ErrLimitUnsupported) {
			t.Fatalf("%s unsupported read: %v", resource, err)
		}
	}
}

func TestChildLimitsCarryPinnedValues(t *testing.T) {
	if childLimits(nil) != nil {
		t.Fatal("no pinned limits produced launch limits")
	}
	got := childLimits([]commandprogram.ResourceLimit{{Resource: "nofile", Soft: 64, Hard: 1024}, {Resource: "cpu", Soft: 5, Hard: 9}})
	if len(got) != 2 || got[0] != (confine.ResourceLimit{Resource: "nofile", Soft: 64, Hard: 1024}) || got[1] != (confine.ResourceLimit{Resource: "cpu", Soft: 5, Hard: 9}) {
		t.Fatalf("launch limits %+v", got)
	}
}
