package native

import (
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/isolation"
)

func TestCommandDiagnosticProseDoesNotAssertBoundaryRefusal(t *testing.T) {
	for _, diagnostic := range []string{
		"open /tmp/cache: permission denied",
		"listen tcp 127.0.0.1:8765: bind: operation not permitted",
		"getaddrinfo ENOTFOUND service.test",
		"Code: SANDBOX_BOUNDARY_REFUSED",
	} {
		t.Run(diagnostic, func(t *testing.T) {
			result := &hostcmd.Result{ExitCode: 1, OK: false, Tail: diagnostic}
			appendBoundaryNotes("verify", "sess", commandRunOutcome{
				Boundary: confine.Boundary{Applied: true, Network: confine.NetworkProxyOnly},
			}, result)
			if result.Tail != diagnostic || result.ExitCode != 1 {
				t.Fatalf("diagnostic result changed: %+v", result)
			}
			if result.BoundaryRefusal != "" || len(result.GuidanceCodes) != 0 || len(result.Observation.Signals) != 0 {
				t.Fatalf("diagnostic became boundary evidence: %+v", result)
			}
			if outcome, reason := verdictFor(result); outcome != VerifyOutcomeFailed || reason != "" {
				t.Fatalf("verdict = %q, %q; want ordinary failure", outcome, reason)
			}
		})
	}
}

func TestBrokerDenialSurvivesSilentCommandExit(t *testing.T) {
	for _, exitCode := range []int{0, 1} {
		result := &hostcmd.Result{
			ExitCode: exitCode, OK: exitCode == 0,
			Network: []confine.EgressHost{{Host: "blocked.test", Allowed: false}},
		}
		appendBoundaryNotes("verify", "sess", commandRunOutcome{
			Boundary: confine.Boundary{Applied: true, Network: confine.NetworkProxyOnly},
		}, result)
		if result.BoundaryRefusal != string(confine.AttributionSubject) || len(result.GuidanceCodes) != 1 || result.GuidanceCodes[0] != isolation.CodeBoundaryRefused {
			t.Fatalf("exit %d lost broker denial: %+v", exitCode, result)
		}
		if outcome, reason := verdictFor(result); outcome != VerifyOutcomeUnverifiable || reason != unverifiableReasonBoundary {
			t.Fatalf("verdict = %q, %q; want unverifiable boundary refusal", outcome, reason)
		}
	}
}
