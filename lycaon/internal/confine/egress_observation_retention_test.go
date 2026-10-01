package confine

import (
	"fmt"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/egressproxy"
	"github.com/lycaon/lycaon/internal/isolation"
)

func TestEndpointObservationCapPreservesRefusals(t *testing.T) {
	for _, tc := range []struct {
		name          string
		priorAllowed  bool
		remotePackage bool
		code          string
	}{
		{"allowed then denied", true, false, isolation.CodeBoundaryRefused},
		{"allowed then package denied", true, true, isolation.CodeRemotePackageDestinationDenied},
		{"registry denied then package denied", false, true, isolation.CodeRemotePackageDestinationDenied},
	} {
		t.Run(tc.name, func(t *testing.T) {
			broker := newTestBroker()
			command := EgressCommand{SessionID: "session", ReducedPackageExecution: tc.remotePackage}
			for i := range maxEgressEndpointsPerCommand {
				command.DeclaredHosts = append(command.DeclaredHosts, fmt.Sprintf("registry-%d.test", i))
			}
			broker.tokens["token"] = command
			for _, host := range command.DeclaredHosts {
				broker.observeEndpoint("token", egressproxy.Endpoint{Host: host, Port: 443}, tc.priorAllowed)
			}
			denied := egressproxy.Endpoint{Host: "blocked.test", Port: 8443}
			broker.observeEndpoint("token", denied, false)
			broker.observeEndpoint("token", denied, false)
			for i := range maxEgressEndpointsPerCommand * 2 {
				broker.observeEndpoint("token", egressproxy.Endpoint{Host: fmt.Sprintf("later-%d.test", i)}, true)
				broker.observeEndpoint("token", egressproxy.Endpoint{Host: fmt.Sprintf("denied-%d.test", i)}, false)
			}
			retained := broker.egress["token"]
			if len(retained) != maxEgressEndpointsPerCommand {
				t.Fatalf("retained %d endpoints, want %d", len(retained), maxEgressEndpointsPerCommand)
			}
			index := slices.IndexFunc(retained, func(host EgressHost) bool {
				return sameEgressEndpoint(host, denied) && !host.Allowed
			})
			if index < 0 || retained[index].Attempts != 2 {
				t.Fatalf("retained observations lost denied endpoint or attempts: %+v", retained)
			}
			context := RefusalContext{MediatedNetwork: retained}
			if tc.remotePackage {
				context.RemotePackageExecution = NewRemotePackageExecutionBoundary(command.DeclaredHosts, nil)
			}
			stamped := StampRefusal("command", command.SessionID,
				Boundary{Applied: true, Network: NetworkProxyOnly}, context)
			if !slices.Equal(stamped.GuidanceCodes, []string{tc.code}) || stamped.Observation.Destination != "blocked.test:8443" {
				t.Fatalf("retained refusal = %+v, want %s for blocked.test:8443", stamped, tc.code)
			}
		})
	}
}
