//go:build linux

package confine

import (
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/confine/linuxsandbox"
)

func TestCapabilityCacheRetriesUnavailableConfinement(t *testing.T) {
	t.Parallel()
	clock := time.Unix(1_700_000_000, 0)
	unavailable := linuxsandbox.Capabilities{Unavailable: []linuxsandbox.Unavailability{{Code: linuxsandbox.UnavailableHelper}}}
	applied := linuxsandbox.Capabilities{Applied: true, UserNamespaces: true, Seccomp: true, Pidfd: true, LandlockABI: linuxsandbox.LandlockABIFloor}
	results := []linuxsandbox.Capabilities{unavailable, applied, unavailable}
	probes := 0
	cache := capabilityCache{
		probe: func() linuxsandbox.Capabilities { probes++; return results[probes-1] },
		now:   func() time.Time { return clock },
	}
	if cache.get().Available() || probes != 1 {
		t.Fatalf("first probe: probes=%d", probes)
	}
	clock = clock.Add(capabilityRetryInterval - time.Second)
	if cache.get().Available() || probes != 1 {
		t.Fatalf("unavailable result re-probed inside the retry interval: probes=%d", probes)
	}
	clock = clock.Add(time.Second)
	if !cache.get().Available() || probes != 2 {
		t.Fatalf("unavailable result not re-probed after the retry interval: probes=%d", probes)
	}
	clock = clock.Add(24 * time.Hour)
	if !cache.get().Available() || probes != 2 {
		t.Fatalf("an applied boundary was re-probed: probes=%d", probes)
	}
}
