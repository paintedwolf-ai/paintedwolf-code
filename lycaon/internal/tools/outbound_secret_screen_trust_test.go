package tools

import (
	"context"
	"testing"

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
	exec := NewDefaultToolExecutor(nil, NewDefaultRegistry(), "implement")
	exec.SetAuthzRecorder(recorder)

	resolution, err := exec.AskSecretScreen(context.Background(), trustedModelAlert())
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
	exec := NewDefaultToolExecutor(nil, NewDefaultRegistry(), "implement")
	exec.SetAuthzRecorder(recorder)
	alert := trustedModelAlert()
	alert.DestinationTrusted = false

	_, err := exec.AskSecretScreen(context.Background(), alert)
	fault, ok := secretmatch.Faulted(err)
	if !ok || fault.Stage != secretmatch.FaultStageCheckpointsUnwired {
		t.Fatalf("err = %v, want the card path", err)
	}
	if len(recorder.records) != 0 {
		t.Fatalf("ledger rows = %+v, want none before a card exists", recorder.records)
	}
}
