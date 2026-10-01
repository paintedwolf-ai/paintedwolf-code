package confine_test

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRemotePackageDeniedDestinationSupersedesGenericSandboxRecovery(t *testing.T) {
	t.Parallel()
	boundary := confine.Boundary{Applied: true, Network: confine.NetworkProxyOnly}
	stamped := confine.StampRefusal(
		"command", "session", boundary,
		confine.RefusalContext{
			RemotePackageExecution: confine.NewRemotePackageExecutionBoundary([]string{"proxy.golang.org"}, nil),
			MediatedNetwork: []confine.EgressHost{
				{Host: "proxy.golang.org", Port: 443, Allowed: true},
				{Host: "gitea.example.com", Port: 8443, Allowed: false},
				{Host: "telemetry.example.com", Port: 8443, Allowed: false},
			},
		},
	)
	if stamped.Attribution != confine.AttributionSubject {
		t.Fatalf("attribution = %q", stamped.Attribution)
	}
	if !slices.Equal(stamped.GuidanceCodes, []string{isolation.CodeRemotePackageDestinationDenied}) {
		t.Fatalf("codes = %v", stamped.GuidanceCodes)
	}
	if stamped.Observation.DenialSubject != confine.DenialSubjectPackageHost {
		t.Fatalf("subject = %q", stamped.Observation.DenialSubject)
	}
	if stamped.Observation.Destination != "gitea.example.com:8443" {
		t.Fatalf("destination = %q", stamped.Observation.Destination)
	}
	if !stamped.Observation.HasSignal(confine.SignalRemotePackageDestinationDenied) {
		t.Fatalf("signals = %v", stamped.Observation.Signals)
	}
}

func TestRemotePackageReviewedRegistryDeniedByStrongerPolicyIsNotMisclassified(t *testing.T) {
	t.Parallel()
	stamped := confine.StampRefusal(
		"command", "session", confine.Boundary{Applied: true, Network: confine.NetworkProxyOnly},
		confine.RefusalContext{
			RemotePackageExecution: confine.NewRemotePackageExecutionBoundary([]string{"proxy.golang.org"}, nil),
			MediatedNetwork:        []confine.EgressHost{{Host: "proxy.golang.org", Allowed: false}},
		},
	)
	if stamped.Attribution != confine.AttributionSubject || !slices.Equal(stamped.GuidanceCodes, []string{isolation.CodeBoundaryRefused}) {
		t.Fatalf("reviewed registry deny misclassified as package scope: %+v", stamped)
	}
}

func TestRemotePackageAllowedRegistryDoesNotStateARefusal(t *testing.T) {
	t.Parallel()
	stamped := confine.StampRefusal(
		"command", "session", confine.Boundary{Applied: true, Network: confine.NetworkProxyOnly},
		confine.RefusalContext{
			RemotePackageExecution: confine.NewRemotePackageExecutionBoundary(nil, nil),
			MediatedNetwork:        []confine.EgressHost{{Host: "proxy.golang.org", Allowed: true}},
		},
	)
	if stamped.Attribution != "" || len(stamped.GuidanceCodes) != 0 {
		t.Fatalf("allowed registry stamped refusal: %+v", stamped)
	}
}

func TestRemotePackageReportStatesTheCompleteAppliedBoundary(t *testing.T) {
	t.Parallel()
	report := confine.ReportOf(confine.Boundary{Applied: true, Network: confine.NetworkProxyOnly}).
		WithRemotePackageExecution([]string{"sum.golang.org", "proxy.golang.org", "sum.golang.org"}, nil)
	payload, err := json.Marshal(report)
	testutil.FailErr(t, "marshal report", err)
	var decoded struct {
		Remote struct {
			Environment               string   `json:"environment"`
			AmbientCredentialsRemoved bool     `json:"ambient_credentials_removed"`
			ProtectedReadsDenied      bool     `json:"protected_reads_denied"`
			NetworkScope              string   `json:"network_scope"`
			AllowedHosts              []string `json:"allowed_hosts"`
		} `json:"remote_package_execution"`
	}
	testutil.FailErr(t, "decode report", json.Unmarshal(payload, &decoded))
	if decoded.Remote.Environment != confine.RemotePackageEnvironmentReduced ||
		!decoded.Remote.AmbientCredentialsRemoved || !decoded.Remote.ProtectedReadsDenied ||
		decoded.Remote.NetworkScope != confine.RemotePackageNetworkScopeRegistryOnly {
		t.Fatalf("remote package report = %+v", decoded.Remote)
	}
	if !slices.Equal(decoded.Remote.AllowedHosts, []string{"proxy.golang.org", "sum.golang.org"}) {
		t.Fatalf("allowed hosts = %v", decoded.Remote.AllowedHosts)
	}
}

func TestRemotePackageReportCarriesApprovedReadExceptions(t *testing.T) {
	paths := []string{"/fixture/cacert.pem"}
	report := confine.ReportOf(confine.Boundary{Applied: true}).WithRemotePackageExecution([]string{"pypi.org"}, paths)
	paths[0] = "/fixture/unapproved"
	if got := report.RemotePackageExecution.ApprovedReadPaths; len(got) != 1 || got[0] != "/fixture/cacert.pem" {
		t.Fatalf("reported approved reads=%v", got)
	}
	payload, err := json.Marshal(report)
	testutil.FailErr(t, "marshal read exception", err)
	var decoded confine.Report
	testutil.FailErr(t, "decode read exception", json.Unmarshal(payload, &decoded))
	if got := decoded.RemotePackageExecution.ApprovedReadPaths; len(got) != 1 || got[0] != "/fixture/cacert.pem" {
		t.Fatalf("wire approved reads=%v", got)
	}
}
