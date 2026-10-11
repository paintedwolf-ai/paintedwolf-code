package terminal

import (
	"testing"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/internal/tools"
)

func appliedBoundary() confine.Boundary {
	return confine.Boundary{Applied: true, Network: confine.NetworkProxyOnly}
}

func TestTerminalObservationsCarryBrokerDenials(t *testing.T) {
	network := []confine.EgressHost{{Host: "blocked.test", Port: 8765, Allowed: false}}
	surfaces := map[string]observation{
		SnapshotToolName: snapshotObservation(bgprocess.PTYSnapshotResult{Boundary: appliedBoundary(), Network: network}),
		OpenToolName:     snapshotObservation(bgprocess.PTYSnapshotResult{Boundary: appliedBoundary(), Network: network}),
		ReadToolName:     readObservation(bgprocess.PTYReadResult{Boundary: appliedBoundary(), Network: network}),
		CloseToolName:    closeObservation(bgprocess.PTYCloseResult{Boundary: appliedBoundary(), Network: network}),
	}
	for tool, obs := range surfaces {
		t.Run(tool, func(t *testing.T) {
			out := &tools.ToolInvocationOut{}
			report := stampBoundary(tools.ToolContext{
				Identity: tools.InvocationIdentity{SessionID: "sess"},
				Effects:  tools.InvocationEffects{Out: out},
			}, tool, obs)
			if report.BoundaryRefusal != string(confine.AttributionSubject) {
				t.Fatalf("boundary refusal = %q", report.BoundaryRefusal)
			}
			if !out.Facts.HasCode(isolation.CodeBoundaryRefused) {
				t.Fatalf("codes = %v", out.Facts.Codes)
			}
			if out.Facts.Confine.Port != 8765 || out.Facts.Confine.Destination != "blocked.test:8765" {
				t.Fatalf("broker destination = %+v", out.Facts.Confine)
			}
		})
	}
}

func TestReportStatesTheBoxOnACleanObservation(t *testing.T) {
	spawn := confine.ReportOf(appliedBoundary()).WithLocalNetwork(confine.LocalNetworkGrant{
		Listen:      true,
		ListenPorts: []uint16{8765},
	})
	out := &tools.ToolInvocationOut{}
	report := stampBoundary(tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: "sess"},
		Effects:  tools.InvocationEffects{Out: out},
	}, SnapshotToolName,
		snapshotObservation(bgprocess.PTYSnapshotResult{
			Boundary: appliedBoundary(),
			Report:   spawn,
		}))

	if !report.Confined {
		t.Fatal("clean observation dropped the confined fact")
	}
	if report.NetworkMode != confine.NetworkLabel(confine.NetworkProxyOnly) {
		t.Fatalf("network mode = %q", report.NetworkMode)
	}
	if !report.ListenGranted || len(report.ListenPorts) != 1 || report.ListenPorts[0] != 8765 {
		t.Fatalf("listener grant not stated: %+v", report)
	}
	if report.BoundaryRefusal != "" {
		t.Fatalf("clean observation attributed a refusal: %q", report.BoundaryRefusal)
	}
	if len(out.Facts.Codes) != 0 {
		t.Fatalf("clean observation stated codes: %v", out.Facts.Codes)
	}
}

// An unconfined terminal has no boundary to attribute anything to.
func TestUnconfinedTerminalAttributesNothing(t *testing.T) {
	out := &tools.ToolInvocationOut{}
	report := stampBoundary(tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: "sess"},
		Effects:  tools.InvocationEffects{Out: out},
	}, SnapshotToolName,
		snapshotObservation(bgprocess.PTYSnapshotResult{
			Network: []confine.EgressHost{{Host: "blocked.test", Allowed: false}},
		}))

	if report.Confined {
		t.Fatal("unconfined terminal reported as confined")
	}
	if report.BoundaryRefusal != "" || len(out.Facts.Codes) != 0 {
		t.Fatalf("unconfined terminal stated a refusal: %q %v", report.BoundaryRefusal, out.Facts.Codes)
	}
}

func TestTerminalObservationStatesRemotePackageDestinationRecovery(t *testing.T) {
	report := confine.ReportOf(appliedBoundary()).WithRemotePackageExecution([]string{"proxy.golang.org"}, nil)
	out := &tools.ToolInvocationOut{}
	got := stampBoundary(tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: "sess"},
		Effects:  tools.InvocationEffects{Out: out},
	}, ReadToolName, observation{
		Boundary: appliedBoundary(), Report: report,
		Network: []confine.EgressHost{{Host: "gitea.example.com", Port: 443, Allowed: false}},
	})
	if got.BoundaryRefusal != string(confine.AttributionSubject) {
		t.Fatalf("boundary refusal = %q", got.BoundaryRefusal)
	}
	if out.Facts.PrimaryCode() != isolation.CodeRemotePackageDestinationDenied {
		t.Fatalf("codes = %v", out.Facts.Codes)
	}
	if out.Facts.Confine.Destination != "gitea.example.com" {
		t.Fatalf("destination = %q", out.Facts.Confine.Destination)
	}
}
