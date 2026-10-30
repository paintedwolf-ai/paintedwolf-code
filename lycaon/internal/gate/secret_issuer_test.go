package gate

import (
	"github.com/lycaon/lycaon/pkg/api"
	"testing"
)

func TestUnverifiedIssuerRequiresInvocationOnlyReview(t *testing.T) {
	for _, posture := range []Posture{PostureLight, PostureBalanced, PostureStrict} {
		t.Run(string(posture), func(t *testing.T) {
			facts := baseFacts(StagePreSend)
			facts.Payload = &SecretHit{Surface: "http_request", RuleID: "managed-secret", RuleTitle: "Protected value", IssuerUnverified: true, DestinationTrusted: true, ChatGenerated: true, RecipientsLocal: true, Source: SecretSourcePublicInbound}
			verdict, decision := Evaluate(facts, posture)
			if verdict != Ask || decision.Primary != api.GateSecretOutbound || decision.Reuse().Offered() || decision.OffersQuiet() {
				t.Fatalf("unverified issuer acquired standing disclosure: %s %+v", verdict, decision)
			}
			if posture.ReleasesChatSecretLocally(facts.Payload) {
				t.Fatal("issuer mismatch inherited local chat release")
			}
		})
	}
}
