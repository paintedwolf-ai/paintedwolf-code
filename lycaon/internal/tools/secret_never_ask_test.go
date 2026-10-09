package tools_test

import (
	"context"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"testing"

	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
)

func neverAskExecutor(t *testing.T, gate *secretReleaseGateStub) *toolexecution.Executor {
	t.Helper()
	exec := toolexecution.NewExecutor(nil, tools.NewDefaultRegistry(), "implement")
	exec.Approvals.SetCheckpointManager(&secretScreenHITL{status: hitl.DecisionStatusApproved}, gate)
	return exec
}

func neverAskAlert(surface secretmatch.ScreenSurface) secretmatch.Alert {
	return secretmatch.Alert{
		SessionID: "sess", ProjectID: "project-id", ProjectDir: "/project",
		Surface:       surface,
		DestinationID: "provider", DestinationLabel: "Provider",
		RuleID: "rule", RuleTitle: "Credential",
		Fingerprints: []secretmatch.SecretFingerprint{"sf1_value"},
	}
}

func TestUnaskedScreenHonorsStandingRedaction(t *testing.T) {
	exec := neverAskExecutor(t, &secretReleaseGateStub{standingRedact: true})
	got, err := exec.Secrets.ResolveSecretScreenUnasked(context.Background(), neverAskAlert(secretmatch.SurfaceHTTPRequest))
	testutil.FailErr(t, "resolve unasked secret screen", err)
	if got.Decision != secretmatch.SendRedacted {
		t.Fatalf("decision = %q, want %q", got.Decision, secretmatch.SendRedacted)
	}
	if got.ReceiptToken == "" {
		t.Fatal("a redacted send must mint a contest receipt")
	}
}

func TestUnaskedScreenWithoutStandingRedactionSendsUnchanged(t *testing.T) {
	exec := neverAskExecutor(t, &secretReleaseGateStub{})
	got, err := exec.Secrets.ResolveSecretScreenUnasked(context.Background(), neverAskAlert(secretmatch.SurfaceHTTPRequest))
	testutil.FailErr(t, "resolve unasked secret screen", err)
	if got.Decision != secretmatch.SendUnchanged {
		t.Fatalf("decision = %q, want %q", got.Decision, secretmatch.SendUnchanged)
	}
}

func TestUnaskedScreenHoldsWhenTheSurfaceCannotRewrite(t *testing.T) {
	exec := neverAskExecutor(t, &secretReleaseGateStub{standingRedact: true})
	got, err := exec.Secrets.ResolveSecretScreenUnasked(context.Background(), neverAskAlert(secretmatch.SurfaceCommand))
	if err == nil {
		t.Fatalf("resolution = %+v, want a held send", got)
	}
	fault, ok := secretmatch.Faulted(err)
	if !ok || fault.Stage != secretmatch.FaultStageRedactUnsupported {
		t.Fatalf("err = %v, want stage %q", err, secretmatch.FaultStageRedactUnsupported)
	}
	if got.Decision != "" {
		t.Fatalf("a held send recorded a decision: %q", got.Decision)
	}
}
