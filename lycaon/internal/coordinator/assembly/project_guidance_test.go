package assembly

import (
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestProjectGuidanceDeduplicatesOnlySameTrustedObservation(t *testing.T) {
	policy := api.Message{Content: "root policy", Origin: api.MessageOriginProject, Authority: api.ContentAuthorityDeveloper, TrustTier: api.ContentTrustTierTrusted}
	peer := policy
	peer.Origin = api.MessageOriginPeerAgent
	peer.Authority = api.ContentAuthorityNone
	scoped := policy
	scoped.Content = "root policy\nnested policy"
	got := deduplicateProjectGuidance([]api.Message{policy, peer, policy, scoped})
	if len(got) != 3 || got[0].Authority != api.ContentAuthorityDeveloper || got[1].Origin != api.MessageOriginPeerAgent || got[2].Content != scoped.Content {
		t.Fatalf("lost provenance or scope: %+v", got)
	}
}
