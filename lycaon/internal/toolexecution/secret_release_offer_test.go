package toolexecution

import (
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/secretmatch"
)

// The secret card carries a release ladder (day, chat, project) and a separate
// standing device redaction rung, each with its own grant identity.
func TestSecretCardCarriesBothLadders(t *testing.T) {
	t.Parallel()
	recipients := []secretmatch.Recipient{secretmatch.LocalRecipient}
	fingerprints := []secretmatch.SecretFingerprint{"fp-one"}
	release := secretReleaseLadder("chat-1", "proj-1", "/workspace", recipients, []string{"token"}, false, fingerprints)
	if got := offerRungs(release); !slices.Equal(got, []hitl.ApprovalOptionRung{hitl.ApprovalRungDay, hitl.ApprovalRungChat, hitl.ApprovalRungProject}) {
		t.Fatalf("release rungs = %v, want day, chat, project", got)
	}
	if release[1].Scope != hitl.ApprovalGrantScopeChat || release[1].Grant.ChatSessionID != "chat-1" {
		t.Fatalf("chat rung = scope %s chat %q", release[1].Scope, release[1].Grant.ChatSessionID)
	}
	for _, offer := range release {
		if offer.Grant.Predicate.Category != hitl.ApprovalGrantCategorySecret {
			t.Errorf("release rung %s grants category %q", offer.Rung, offer.Grant.Predicate.Category)
		}
	}
	managed := secretReleaseLadder("chat-1", "proj-1", "/workspace", recipients, []string{"token"}, true, fingerprints)
	if managed[0].Title != hitl.TitleSendFor1Day || release[0].Title != hitl.TitleSendUnchangedFor1Day {
		t.Fatalf("day titles = managed %q, unmanaged %q", managed[0].Title, release[0].Title)
	}
	if got := offerRungs(secretReleaseLadder("", "proj-1", "/workspace", recipients, nil, false, fingerprints)); !slices.Equal(got, []hitl.ApprovalOptionRung{hitl.ApprovalRungDay, hitl.ApprovalRungProject}) {
		t.Fatalf("release rungs without a chat = %v, want day, project", got)
	}

	redact := secretRedactLadder("proj-1", "/workspace", fingerprints)
	if len(redact) != 1 || redact[0].Rung != hitl.ApprovalRungDevice || redact[0].Group != hitl.GroupRedaction ||
		redact[0].Grant.Predicate.Category != hitl.ApprovalGrantCategorySecretRedact {
		t.Fatalf("redact ladder = %+v, want one device redaction rung", redact)
	}

	ids := map[string]bool{}
	for _, offer := range append(release, redact...) {
		if offer.ID == "" || offer.ID != offer.Grant.ID || ids[offer.ID] {
			t.Fatalf("offer %s id %q (grant %q) is empty, split or shared", offer.Rung, offer.ID, offer.Grant.ID)
		}
		ids[offer.ID] = true
	}
	other := secretRedactLadder("proj-1", "/workspace", []secretmatch.SecretFingerprint{"fp-two"})
	if len(other) != 1 || other[0].ID == redact[0].ID {
		t.Fatal("a different secret shares the redaction grant identity")
	}
	if secretRedactLadder("", "/workspace", fingerprints) != nil || secretReleaseLadder("chat-1", "", "/workspace", recipients, nil, false, fingerprints) != nil {
		t.Fatal("a ladder without a project minted offers")
	}
}

func offerRungs(offers []hitl.ApprovalGrantOffer) []hitl.ApprovalOptionRung {
	out := make([]hitl.ApprovalOptionRung, 0, len(offers))
	for _, offer := range offers {
		out = append(out, offer.Rung)
	}
	return out
}
