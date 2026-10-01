package tools

import (
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestApplyRefusalFactsKeepsCompletedOutcome(t *testing.T) {
	t.Parallel()
	stamped := confine.StampedRefusal{
		Attribution:   confine.AttributionSubject,
		GuidanceCodes: []string{isolation.CodeRemotePackageDestinationDenied},
		Observation:   confine.Observation{Applied: true, DenialSubject: confine.DenialSubjectPackageHost},
	}
	facts := ApplyRefusalFacts(guidance.ToolResultFacts{}, stamped)
	if facts.Resolution() != api.ToolResultOutcomeCompleted {
		t.Fatalf("outcome = %q want completed", facts.Resolution())
	}
	if facts.UnstatedNonSuccess() {
		t.Fatal("settled package refusal must state a code")
	}
	if facts.PrimaryCode() != isolation.CodeRemotePackageDestinationDenied {
		t.Fatalf("primary = %q want %q", facts.PrimaryCode(), isolation.CodeRemotePackageDestinationDenied)
	}
	if !facts.Confine.Applied || facts.Confine.DenialSubject != confine.DenialSubjectPackageHost {
		t.Fatalf("confine observation not copied: %+v", facts.Confine)
	}
}

func TestApplyRefusalFactsCopiesBoundaryCode(t *testing.T) {
	t.Parallel()
	stamped := confine.StampRefusal("command", "sess", confine.Boundary{
		Applied: true, Network: confine.NetworkProxyOnly, WriteRoots: []string{"/proj"},
	}, confine.RefusalContext{MediatedNetwork: []confine.EgressHost{{Host: "blocked.test", Allowed: false}}})
	facts := ApplyRefusalFacts(guidance.ToolResultFacts{}, stamped)
	if facts.Resolution() != api.ToolResultOutcomeCompleted {
		t.Fatalf("outcome = %q want completed", facts.Resolution())
	}
	if facts.PrimaryCode() != isolation.CodeBoundaryRefused {
		t.Fatalf("primary = %q want %s", facts.PrimaryCode(), isolation.CodeBoundaryRefused)
	}
}

func TestApplyRefusalFactsCarriesRemotePackageDestinationDetails(t *testing.T) {
	t.Parallel()
	stamped := confine.StampedRefusal{
		Attribution:   confine.AttributionSubject,
		GuidanceCodes: []string{isolation.CodeRemotePackageDestinationDenied},
		Observation: confine.Observation{
			Applied: true, DenialSubject: confine.DenialSubjectPackageHost,
			Destination: "gitea.example.com",
		},
	}
	facts := ApplyRefusalFacts(guidance.ToolResultFacts{}, stamped)
	feedback := facts.FeedbackFor(isolation.CodeRemotePackageDestinationDenied)
	if feedback.Details["destination"] != "gitea.example.com" {
		t.Fatalf("feedback = %+v", feedback)
	}
	if feedback.Subject == nil || feedback.Subject.Kind != "destination" || feedback.Subject.ID != "gitea.example.com" {
		t.Fatalf("subject = %+v", feedback.Subject)
	}
}
