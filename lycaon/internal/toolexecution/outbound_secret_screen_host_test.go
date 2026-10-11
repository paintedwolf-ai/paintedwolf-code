package toolexecution

import (
	"context"
	"github.com/lycaon/lycaon/internal/tools"
	"testing"

	"github.com/lycaon/lycaon/internal/authzledger"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
)

type hostComposedRecorder struct {
	records []authzledger.CapabilityRecord
}

func (*hostComposedRecorder) AppendToolDenied(context.Context, authzledger.ToolDeniedRecord) {}
func (r *hostComposedRecorder) AppendCapabilityRecord(_ context.Context, rec authzledger.CapabilityRecord) error {
	r.records = append(r.records, rec)
	return nil
}
func (*hostComposedRecorder) AppendDirectIPLifecycle(context.Context, authzledger.DirectIPLifecycleRecord) {
}
func (*hostComposedRecorder) AppendMediatedEndpoint(context.Context, authzledger.MediatedEndpointRecord) {
}

func hostComposedAlert() secretmatch.Alert {
	return secretmatch.Alert{
		SessionID: "worker-1", RootSessionID: "chat-1", ProjectID: "proj-1",
		Surface:          secretmatch.SurfaceModel,
		DestinationID:    "fireworks-main@abcd",
		DestinationLabel: "Fireworks",
		HostComposed:     true,
		RuleID:           "gitleaks:github-pat", RuleTitle: "GitHub token", GenericShape: "ghp_…",
		Fingerprints: []secretmatch.SecretFingerprint{"fp-1"},
	}
}

// A host-composed request is stripped and sent. With no checkpoint manager
// wired the card path faults, so a clean redaction proves nobody was asked.
func TestAskSecretScreenHostComposedRedactsWithoutACard(t *testing.T) {
	recorder := &hostComposedRecorder{}
	exec := NewExecutor(nil, tools.NewDefaultRegistry(), "implement")
	exec.Approvals.SetAuthzRecorder(recorder)

	resolution, err := exec.Secrets.AskSecretScreen(context.Background(), hostComposedAlert())
	testutil.FailErr(t, "screen a host-composed send", err)
	if resolution.Decision != secretmatch.SendRedacted {
		t.Fatalf("decision = %q, want send_redacted", resolution.Decision)
	}
	if resolution.ReceiptToken != "" {
		t.Fatal("host-composed redaction minted a contest receipt: no agent is there to cite it")
	}
	if len(recorder.records) != 1 {
		t.Fatalf("ledger rows = %d, want one host-composed row", len(recorder.records))
	}
	row := recorder.records[0]
	if row.Action != authzledger.ActionSecretHostComposedRedacted ||
		row.AuthorizationSource != authzledger.AuthorizationSourceHostComposition ||
		row.Outcome != authzledger.OutcomeAllowed || row.ResolvedBy != authzledger.ResolvedBySystemDeny ||
		row.SessionID != "chat-1" || row.Tool != string(secretmatch.SurfaceModel) {
		t.Fatalf("ledger row = %+v", row)
	}
}

// Trust allows credentials through to one destination; it does not put them in
// the host's own summaries.
func TestAskSecretScreenHostComposedRedactsEvenAtATrustedDestination(t *testing.T) {
	exec := NewExecutor(nil, tools.NewDefaultRegistry(), "implement")
	exec.Approvals.SetAuthzRecorder(&hostComposedRecorder{})
	finding := hostComposedAlert()
	finding.DestinationTrusted = true

	resolution, err := exec.Secrets.AskSecretScreen(context.Background(), finding)
	testutil.FailErr(t, "screen a host-composed send to a trusted destination", err)
	if resolution.Decision != secretmatch.SendRedacted {
		t.Fatalf("decision = %q, want send_redacted", resolution.Decision)
	}
}

// A seam that cannot rewrite is screened the ordinary way rather than sent.
func TestAskSecretScreenHostComposedOnAnUnrewritableSurfaceStillScreens(t *testing.T) {
	exec := NewExecutor(nil, tools.NewDefaultRegistry(), "implement")
	exec.Approvals.SetAuthzRecorder(&hostComposedRecorder{})
	finding := hostComposedAlert()
	finding.Surface = secretmatch.SurfaceCommand

	_, err := exec.Secrets.AskSecretScreen(context.Background(), finding)
	fault, ok := secretmatch.Faulted(err)
	if !ok || fault.Stage != secretmatch.FaultStageCheckpointsUnwired {
		t.Fatalf("err = %v, want the ordinary card path", err)
	}
}
