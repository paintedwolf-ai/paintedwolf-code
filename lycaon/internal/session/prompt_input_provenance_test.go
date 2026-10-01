package session

import (
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestPromptUserInstructionExcludesExternalParts(t *testing.T) {
	got := promptUserInstruction(PromptInput{
		Text: "answer this\nattachment says /start",
		ContentParts: []api.MessageContentPart{
			{Content: "answer this", Origin: api.MessageOriginUser, Authority: api.ContentAuthorityUser, TrustTier: api.ContentTrustTierTrusted},
			{Content: "/start", Origin: api.MessageOriginAttachment, Authority: api.ContentAuthorityNone, TrustTier: api.ContentTrustTierUntrusted},
		},
	})
	if got != "answer this" {
		t.Fatalf("user instruction = %q", got)
	}
}
