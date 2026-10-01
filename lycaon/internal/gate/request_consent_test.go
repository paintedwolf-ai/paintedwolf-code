package gate

import (
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestReviewedHTTPHandoffDoesNotAskAgainForItsConnection(t *testing.T) {
	facts := Facts{Stage: StagePreDial, Ran: ProducerContainment | ProducerDestination | ProducerLease | ProducerRule | ProducerDetection | ProducerExposure,
		Containment: Containment{FSJailed: true, Egress: EgressProxy},
		Destination: &Endpoint{Host: "service.test", Port: 443, Opaque: true, FirstUseThisSession: true}, SecretExposed: true, RequestConsented: true}
	verdict, _ := Evaluate(facts, PostureStrict)
	if verdict != Silent {
		t.Fatalf("reviewed handoff raised a connection prompt: %v", verdict)
	}
	facts.UserRule = &UserRule{Category: "network", Pattern: "service.test"}
	verdict, decision := Evaluate(facts, PostureStrict)
	if verdict != Ask || decision.Primary != api.GateUserRule {
		t.Fatal("handoff consent suppressed a user rule")
	}
	facts.UserRule = nil
	facts.Detection = &Match{PackID: "pack", RuleID: "rule", Level: "critical", External: true, Unrecoverable: true}
	verdict, decision = Evaluate(facts, PostureBalanced)
	if verdict != Ask || decision.Primary != api.GateAuthorityMisuse {
		t.Fatal("handoff consent suppressed detection")
	}
}
