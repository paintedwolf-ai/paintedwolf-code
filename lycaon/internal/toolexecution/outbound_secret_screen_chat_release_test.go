package toolexecution

import (
	"context"
	"github.com/lycaon/lycaon/internal/tools"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/authzledger"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/presence"
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
		RecipientsLocal: true,
	}
}

// screenedValue is the invocation's resolution of the alert's one value.
func screenedValue(custody secretcap.Custody, chatGenerated bool) *secretcap.Resolution {
	return secretcap.NewResolutionForTest(map[string]any{"value": "{{ref}}"}, []secretcap.TestResolvedValue{{
		ID: "11111111-1111-4111-8111-111111111111", Name: "todos-postgres-password", Value: "screened-chat-value",
		Path: "/value", Version: 1, Custody: custody, Fingerprint: "fp-chat", ChatGenerated: chatGenerated,
	}})
}

func chatReleaseExecutor(posture gate.Posture) (*Executor, *capabilityRecorder) {
	recorder := &capabilityRecorder{}
	exec := NewExecutor(nil, tools.NewDefaultRegistry(), "implement")
	exec.Approvals.SetAuthzRecorder(recorder)
	exec.Network.SetEgressPostureSource(func(string) gate.Posture { return posture })
	return exec, recorder
}

// Below Strict, a chat's generated secret handed to local recipients sends
// with no card, and the ledger names the posture release.
func TestAskSecretScreenReleasesChatSecretsLocallyBelowStrict(t *testing.T) {
	for _, posture := range []gate.Posture{gate.PostureLight, gate.PostureBalanced} {
		t.Run(string(posture), func(t *testing.T) {
			exec, recorder := chatReleaseExecutor(posture)
			resolution := screenedValue(secretcap.CustodyChat, true)
			ctx := secretcap.WithResolution(context.Background(), resolution)

			got, err := exec.Secrets.AskSecretScreen(ctx, chatSecretAlert())
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
		value   *secretcap.Resolution
	}{
		"strict":           {gate.PostureStrict, func(*secretmatch.Alert) {}, screenedValue(secretcap.CustodyChat, true)},
		"remote recipient": {gate.PostureLight, func(a *secretmatch.Alert) { a.RecipientsLocal = false }, screenedValue(secretcap.CustodyChat, true)},
		"host value":       {gate.PostureLight, func(*secretmatch.Alert) {}, screenedValue(secretcap.CustodyHost, false)},
		"unresolved bytes": {gate.PostureLight, func(*secretmatch.Alert) {}, nil},
	} {
		t.Run(name, func(t *testing.T) {
			exec, recorder := chatReleaseExecutor(tt.posture)
			alert := chatSecretAlert()
			tt.edit(&alert)
			_, err := exec.Secrets.AskSecretScreen(secretcap.WithResolution(context.Background(), tt.value), alert)
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

// A value a person stored is never released silently: at every posture and for
// local recipients it asks, and a device without presence refuses outright.
func TestAskSecretScreenNeverReleasesAHeldValueSilently(t *testing.T) {
	for _, posture := range []gate.Posture{gate.PostureLight, gate.PostureBalanced, gate.PostureStrict} {
		t.Run(string(posture), func(t *testing.T) {
			exec, recorder := chatReleaseExecutor(posture)
			ctx := secretcap.WithResolution(context.Background(), screenedValue(secretcap.CustodyPerson, false))
			_, err := exec.Secrets.AskSecretScreen(ctx, chatSecretAlert())
			if fault, ok := secretmatch.Faulted(err); !ok || fault.Stage != secretmatch.FaultStagePresenceUnavailable {
				t.Fatalf("held value without presence err = %v", err)
			}
			exec.Secrets.SetPresenceAvailable(func() bool { return true })
			_, err = exec.Secrets.AskSecretScreen(ctx, chatSecretAlert())
			if fault, ok := secretmatch.Faulted(err); !ok || fault.Stage != secretmatch.FaultStageCheckpointsUnwired {
				t.Fatalf("held value with presence err = %v, want the card path", err)
			}
			if len(recorder.records) != 0 {
				t.Fatalf("ledger rows = %+v, want none", recorder.records)
			}
		})
	}
}

// Approvals disabled at device scope do not disable custody.
func TestUnaskedScreenStillAsksForAHeldValue(t *testing.T) {
	exec, _ := chatReleaseExecutor(gate.PostureLight)
	ctx := secretcap.WithResolution(context.Background(), screenedValue(secretcap.CustodyPerson, false))
	_, err := exec.Secrets.ResolveSecretScreenUnasked(ctx, chatSecretAlert())
	if fault, ok := secretmatch.Faulted(err); !ok || fault.Stage != secretmatch.FaultStagePresenceUnavailable {
		t.Fatalf("unasked held value err = %v", err)
	}
}

// A held value whose recipients this chat already approved still waits on
// the chat's unlock: a locked chat takes the unlock card's path, an unlocked
// one sends with no card.
func TestApprovedHeldValueWaitsOnlyForTheUnlock(t *testing.T) {
	for name, unlocked := range map[string]bool{"locked": false, "unlocked": true} {
		t.Run(name, func(t *testing.T) {
			exec, _ := chatReleaseExecutor(gate.PostureLight)
			exec.Secrets.SetPresenceAvailable(func() bool { return true })
			unlocks := presence.NewUnlocks()
			exec.Secrets.SetVaultUnlocks(unlocks)
			if unlocked {
				unlocks.Open(presence.NewUnlock("root-1", presence.Verified{
					ChallengeID: "22222222-2222-4222-8222-222222222222", PersonID: "owner",
					Authenticator: presence.AuthenticatorMacOS, WindowLabel: "main", VerifiedAt: time.Now(),
				}))
			}
			resolution := screenedValue(secretcap.CustodyPerson, false)
			alert := chatSecretAlert()
			recipients, err := secretScreenRecipients(alert)
			testutil.FailErr(t, "recipients", err)
			resolution.ApproveRelease(secretcap.Release{Fingerprints: alert.Fingerprints, Recipients: recipients})

			got, err := exec.Secrets.AskSecretScreen(secretcap.WithResolution(context.Background(), resolution), alert)
			if !unlocked {
				if fault, ok := secretmatch.Faulted(err); !ok || fault.Stage != secretmatch.FaultStageCheckpointsUnwired {
					t.Fatalf("locked chat err = %v, want the unlock card's path", err)
				}
				return
			}
			testutil.FailErr(t, "unlocked send", err)
			if got.Decision != secretmatch.SendUnchanged {
				t.Fatalf("decision = %q", got.Decision)
			}
		})
	}
}

// A send that needs only the unlock waits behind a card already open in its
// chat instead of raising a second one, and proceeds when that card unlocks
// the chat. If the person refuses that card, the send is held with it; if
// the card settles otherwise without unlocking, the send asks itself.
func TestApprovedHeldValueWaitsBehindTheChatsOpenCard(t *testing.T) {
	for name, tt := range map[string]struct{ unlocks, refused bool }{
		"card unlocks":   {unlocks: true},
		"card does not":  {},
		"person refuses": {refused: true},
	} {
		t.Run(name, func(t *testing.T) {
			exec, _ := chatReleaseExecutor(gate.PostureLight)
			exec.Secrets.SetPresenceAvailable(func() bool { return true })
			registry := presence.NewUnlocks()
			exec.Secrets.SetVaultUnlocks(registry)
			closeCard := exec.Secrets.heldAsks.open("root-1")
			go func() {
				time.Sleep(2 * heldAskPoll)
				if tt.unlocks {
					registry.Open(presence.NewUnlock("root-1", presence.Verified{
						ChallengeID: "33333333-3333-4333-8333-333333333333", PersonID: "owner",
						Authenticator: presence.AuthenticatorMacOS, WindowLabel: "main", VerifiedAt: time.Now(),
					}))
				}
				closeCard(tt.refused)
			}()
			resolution := screenedValue(secretcap.CustodyPerson, false)
			alert := chatSecretAlert()
			recipients, err := secretScreenRecipients(alert)
			testutil.FailErr(t, "recipients", err)
			resolution.ApproveRelease(secretcap.Release{Fingerprints: alert.Fingerprints, Recipients: recipients})

			got, err := exec.Secrets.AskSecretScreen(secretcap.WithResolution(context.Background(), resolution), alert)
			if tt.unlocks || tt.refused {
				testutil.FailErr(t, "send behind the open card", err)
				want := secretmatch.SendUnchanged
				if tt.refused {
					want = secretmatch.Withhold
				}
				if got.Decision != want {
					t.Fatalf("decision = %q, want %q", got.Decision, want)
				}
				return
			}
			if fault, ok := secretmatch.Faulted(err); !ok || fault.Stage != secretmatch.FaultStageCheckpointsUnwired {
				t.Fatalf("err = %v, want the unlock card's path", err)
			}
		})
	}
}
