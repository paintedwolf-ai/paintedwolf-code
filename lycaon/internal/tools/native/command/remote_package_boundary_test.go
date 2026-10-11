package command

import (
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/isolation"
)

func TestForegroundResultTurnsDeniedPackageHostIntoDedicatedRecovery(t *testing.T) {
	t.Parallel()
	boundary := confine.Boundary{Applied: true, Network: confine.NetworkProxyOnly}
	result := &hostcmd.Result{
		Network: []confine.EgressHost{{Host: "gitea.example.com", Port: 443, Allowed: false}},
		Report:  confine.ReportOf(boundary).WithRemotePackageExecution([]string{"proxy.golang.org"}, nil),
	}
	appendBoundaryNotes("command", "session", RunOutcome{Boundary: boundary}, result)
	if result.BoundaryRefusal != string(confine.AttributionSubject) {
		t.Fatalf("boundary refusal = %q", result.BoundaryRefusal)
	}
	if len(result.GuidanceCodes) != 1 || result.GuidanceCodes[0] != isolation.CodeRemotePackageDestinationDenied {
		t.Fatalf("codes = %v", result.GuidanceCodes)
	}
	if result.Observation.Destination != "gitea.example.com" {
		t.Fatalf("destination = %q", result.Observation.Destination)
	}
}

func TestForegroundResultKeepsAllowedPackageRegistryQuiet(t *testing.T) {
	t.Parallel()
	boundary := confine.Boundary{Applied: true, Network: confine.NetworkProxyOnly}
	result := &hostcmd.Result{
		Network: []confine.EgressHost{{Host: "proxy.golang.org", Port: 443, Allowed: true}},
		Report:  confine.ReportOf(boundary).WithRemotePackageExecution([]string{"proxy.golang.org"}, nil),
	}
	appendBoundaryNotes("command", "session", RunOutcome{Boundary: boundary}, result)
	if result.BoundaryRefusal != "" || len(result.GuidanceCodes) != 0 {
		t.Fatalf("allowed registry stamped refusal: %+v", result)
	}
}
