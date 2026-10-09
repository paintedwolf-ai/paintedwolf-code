package command

import (
	"testing"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/internal/tools"
)

func TestBackgroundOutputPreservesRemotePackageBoundaryAndRecovery(t *testing.T) {
	t.Parallel()
	boundary := confine.Boundary{Applied: true, Network: confine.NetworkProxyOnly}
	report := confine.ReportOf(boundary).WithRemotePackageExecution([]string{"proxy.golang.org"}, nil)
	outFacts := &tools.ToolInvocationOut{}
	payload := observeBackgroundOutput(
		tools.ToolContext{SessionID: "session", Out: outFacts},
		commandOutput{Handle: "process", Running: false},
		boundary,
		confine.SpawnFacts{
			Report: report,
			Network: func() []confine.EgressHost {
				return []confine.EgressHost{{Host: "gitea.example.com", Port: 443, Allowed: false}}
			},
		},
	)
	if payload.RemotePackageExecution == nil ||
		payload.RemotePackageExecution.NetworkScope != confine.RemotePackageNetworkScopeRegistryOnly {
		t.Fatalf("remote package boundary = %+v", payload.RemotePackageExecution)
	}
	if payload.BoundaryRefusal != string(confine.AttributionSubject) {
		t.Fatalf("boundary refusal = %q", payload.BoundaryRefusal)
	}
	if outFacts.Facts.PrimaryCode() != isolation.CodeRemotePackageDestinationDenied {
		t.Fatalf("codes = %v", outFacts.Facts.Codes)
	}
	if outFacts.Facts.Confine.Destination != "gitea.example.com" {
		t.Fatalf("destination = %q", outFacts.Facts.Confine.Destination)
	}
}

func TestBackgroundOutputDoesNotClassifyOrdinaryLiveOutputAsRefusal(t *testing.T) {
	t.Parallel()
	boundary := confine.Boundary{Applied: true, Network: confine.NetworkProxyOnly}
	outFacts := &tools.ToolInvocationOut{}
	payload := observeBackgroundOutput(
		tools.ToolContext{SessionID: "session", Out: outFacts},
		commandOutput{
			Handle:  "process",
			Running: true,
			Chunks:  []bgprocess.OutputChunk{{Text: "permission denied by the remote service"}},
		},
		boundary,
		confine.SpawnFacts{Report: confine.ReportOf(boundary)},
	)
	if payload.BoundaryRefusal != "" {
		t.Fatalf("boundary refusal = %q", payload.BoundaryRefusal)
	}
	if len(outFacts.Facts.Codes) != 0 {
		t.Fatalf("codes = %v", outFacts.Facts.Codes)
	}
}
