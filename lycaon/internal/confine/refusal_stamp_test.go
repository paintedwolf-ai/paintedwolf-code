package confine_test

import (
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/isolation"
)

func TestStampRefusalRequiresObservedBrokerDenial(t *testing.T) {
	for _, tc := range []struct {
		name    string
		applied bool
		hosts   []confine.EgressHost
		want    bool
	}{
		{name: "no observation", applied: true},
		{name: "allowed connection", applied: true, hosts: []confine.EgressHost{{Host: "service.test", Allowed: true}}},
		{name: "upstream refused connection", applied: true, hosts: []confine.EgressHost{{Host: "service.test", Allowed: true, Outcome: "connect_failed", DialError: "permission denied"}}},
		{name: "unapplied boundary", hosts: []confine.EgressHost{{Host: "service.test", Allowed: false}}},
		{name: "unnamed observation", applied: true, hosts: []confine.EgressHost{{Allowed: false}}},
		{name: "broker denied connection", applied: true, hosts: []confine.EgressHost{{Host: "service.test", Port: 8443, Allowed: false}}, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stamped := confine.StampRefusal("command", "sess",
				confine.Boundary{Applied: tc.applied, Network: confine.NetworkProxyOnly},
				confine.RefusalContext{MediatedNetwork: tc.hosts})
			if stamped.Observation.HasSignal(confine.SignalBoundaryRefused) != tc.want {
				t.Fatalf("observation = %+v, want refusal %t", stamped.Observation, tc.want)
			}
			if !tc.want {
				if stamped.Attribution != "" || len(stamped.GuidanceCodes) != 0 || stamped.Observation.Destination != "" {
					t.Fatalf("unobserved refusal: %+v", stamped)
				}
				return
			}
			if stamped.Attribution != confine.AttributionSubject || !slices.Equal(stamped.GuidanceCodes, []string{isolation.CodeBoundaryRefused}) {
				t.Fatalf("broker refusal = %+v", stamped)
			}
			if stamped.Observation.Destination != "service.test:8443" || stamped.Observation.Port != 8443 {
				t.Fatalf("broker destination = %+v", stamped.Observation)
			}
		})
	}
}

func TestStampRefusalPreservesAppliedSocksFactWithoutClaimingFailure(t *testing.T) {
	stamped := confine.StampRefusal("command", "sess",
		confine.Boundary{Applied: true, Network: confine.NetworkProxyOnly, SocksProxyEnv: true},
		confine.RefusalContext{})
	if !slices.Equal(stamped.Observation.Signals, []string{confine.SignalSocksProxy}) || stamped.Attribution != "" {
		t.Fatalf("applied boundary = %+v", stamped)
	}
}
