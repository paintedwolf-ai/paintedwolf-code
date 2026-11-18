package toolexecution

import (
	"context"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/tools"

	"github.com/lycaon/lycaon/internal/authzledger"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
)

type capabilityRecorder struct {
	records []authzledger.CapabilityRecord
}

func (*capabilityRecorder) AppendToolDenied(context.Context, authzledger.ToolDeniedRecord) {}
func (r *capabilityRecorder) AppendCapabilityRecord(_ context.Context, rec authzledger.CapabilityRecord) error {
	r.records = append(r.records, rec)
	return nil
}
func (*capabilityRecorder) AppendDirectIPLifecycle(context.Context, authzledger.DirectIPLifecycleRecord) {
}
func (*capabilityRecorder) AppendMediatedEndpoint(context.Context, authzledger.MediatedEndpointRecord) {
}

func trustedModelAlert() secretmatch.Alert {
	return secretmatch.Alert{
		SessionID: "sess-1", RootSessionID: "root-1", ProjectID: "proj-1",
		Surface:            secretmatch.SurfaceModel,
		DestinationID:      "ollama-1@abcd",
		DestinationLabel:   "Ollama",
		DestinationTrusted: true,
		RuleID:             "gitleaks:github-pat", RuleTitle: "GitHub token", GenericShape: "ghp_…",
		Fingerprints: []secretmatch.SecretFingerprint{"fp-1"},
	}
}

// A trusted destination sends unchanged with no card, and the ledger still
// counts the send under the trust that authorized it.
func TestAskSecretScreenTrustedDestinationSendsUnchangedAndRecordsTheSend(t *testing.T) {
	recorder := &capabilityRecorder{}
	exec := NewExecutor(nil, tools.NewDefaultRegistry(), "implement")
	exec.Approvals.SetAuthzRecorder(recorder)

	resolution, err := exec.Secrets.AskSecretScreen(context.Background(), trustedModelAlert())
	testutil.FailErr(t, "ask for a trusted destination", err)
	if resolution.Decision != secretmatch.SendUnchanged {
		t.Fatalf("decision = %q, want send_unchanged", resolution.Decision)
	}
	if len(recorder.records) != 1 {
		t.Fatalf("ledger rows = %d, want one trusted-destination row", len(recorder.records))
	}
	row := recorder.records[0]
	if row.Action != authzledger.ActionSecretDestinationTrusted ||
		row.AuthorizationSource != authzledger.AuthorizationSourceTrustedDestination ||
		row.Outcome != authzledger.OutcomeAllowed || row.ResolvedBy != authzledger.ResolvedByHuman ||
		row.SessionID != "root-1" || row.Tool != string(secretmatch.SurfaceModel) {
		t.Fatalf("ledger row = %+v", row)
	}
}

// Without trust the same alert reaches the card path, which faults with no
// checkpoint manager.
func TestAskSecretScreenUntrustedDestinationReachesTheCard(t *testing.T) {
	recorder := &capabilityRecorder{}
	exec := NewExecutor(nil, tools.NewDefaultRegistry(), "implement")
	exec.Approvals.SetAuthzRecorder(recorder)
	alert := trustedModelAlert()
	alert.DestinationTrusted = false

	_, err := exec.Secrets.AskSecretScreen(context.Background(), alert)
	fault, ok := secretmatch.Faulted(err)
	if !ok || fault.Stage != secretmatch.FaultStageCheckpointsUnwired {
		t.Fatalf("err = %v, want the card path", err)
	}
	if len(recorder.records) != 0 {
		t.Fatalf("ledger rows = %+v, want none before a card exists", recorder.records)
	}
}

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
