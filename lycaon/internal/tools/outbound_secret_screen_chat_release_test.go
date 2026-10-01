package tools

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/authzledger"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
)

func chatSecretAlert() secretmatch.Alert {
	return secretmatch.Alert{
		SessionID: "sess-1", RootSessionID: "root-1", ProjectID: "proj-1",
		Surface:          secretmatch.SurfaceCommand,
		DestinationID:    "proxy",
		DestinationLabel: "chat processes with mediated network access",
		RuleID:           secretmatch.ManagedRuleID, RuleTitle: secretmatch.ManagedRuleTitle, GenericShape: "Protected value",
		SecretNames:     []string{"todos-postgres-password"},
		Fingerprints:    []secretmatch.SecretFingerprint{"fp-chat"},
		ChatGenerated:   true,
		RecipientsLocal: true,
	}
}

func chatReleaseExecutor(posture gate.Posture) (*DefaultToolExecutor, *capabilityRecorder) {
	recorder := &capabilityRecorder{}
	exec := NewDefaultToolExecutor(nil, NewDefaultRegistry(), "implement")
	exec.SetAuthzRecorder(recorder)
	exec.SetEgressPostureSource(func(string) gate.Posture { return posture })
	return exec, recorder
}

// Below Strict, a chat's generated secret handed to local recipients sends
// with no card, and the ledger names the posture release.
func TestAskSecretScreenReleasesChatSecretsLocallyBelowStrict(t *testing.T) {
	for _, posture := range []gate.Posture{gate.PostureLight, gate.PostureBalanced} {
		t.Run(string(posture), func(t *testing.T) {
			exec, recorder := chatReleaseExecutor(posture)
			resolution := &secretcap.Resolution{}
			ctx := secretcap.WithResolution(context.Background(), resolution)

			got, err := exec.AskSecretScreen(ctx, chatSecretAlert())
			testutil.FailErr(t, "ask for a chat-local release", err)
			if got.Decision != secretmatch.SendUnchanged {
				t.Fatalf("decision = %q, want send_unchanged", got.Decision)
			}
			if len(recorder.records) != 1 {
				t.Fatalf("ledger rows = %d, want one release row", len(recorder.records))
			}
			row := recorder.records[0]
			if row.Action != authzledger.ActionSecretChatLocalRelease ||
				row.AuthorizationSource != authzledger.AuthorizationSourcePolicy ||
				row.ResolvedBy != authzledger.ResolvedByPolicy || row.Outcome != authzledger.OutcomeAllowed ||
				row.SessionID != "root-1" {
				t.Fatalf("ledger row = %+v", row)
			}
			// No one reviewed the recipients, so no connection consent follows.
			recipient := secretmatch.Recipient{ID: "proxy", Surface: secretmatch.SurfaceCommand, Kind: secretmatch.DestinationProcess}
			if resolution.RecipientApproved(recipient) || resolution.UseCovered("fp-chat", recipient) {
				t.Fatal("a posture release was recorded as a reviewed handoff")
			}
		})
	}
}

// Strict, a remote recipient, or a value the host did not generate for this
// chat all reach the card path, which faults with no checkpoint manager.
func TestAskSecretScreenAsksForEveryOtherChatSecretRelease(t *testing.T) {
	for name, tt := range map[string]struct {
		posture gate.Posture
		edit    func(*secretmatch.Alert)
	}{
		"strict":           {gate.PostureStrict, func(*secretmatch.Alert) {}},
		"remote recipient": {gate.PostureLight, func(a *secretmatch.Alert) { a.RecipientsLocal = false }},
		"person's secret":  {gate.PostureLight, func(a *secretmatch.Alert) { a.ChatGenerated = false }},
	} {
		t.Run(name, func(t *testing.T) {
			exec, recorder := chatReleaseExecutor(tt.posture)
			alert := chatSecretAlert()
			tt.edit(&alert)
			_, err := exec.AskSecretScreen(context.Background(), alert)
			fault, ok := secretmatch.Faulted(err)
			if !ok || fault.Stage != secretmatch.FaultStageCheckpointsUnwired {
				t.Fatalf("err = %v, want the card path", err)
			}
			if len(recorder.records) != 0 {
				t.Fatalf("ledger rows = %+v, want none before a card exists", recorder.records)
			}
		})
	}
}
