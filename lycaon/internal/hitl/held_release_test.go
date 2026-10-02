package hitl_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/presence"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type recordedUnlocks struct{ unlocks []presence.Unlock }

func (r *recordedUnlocks) RecordUnlockTx(_ context.Context, _ *sql.Tx, _ string, unlock presence.Unlock) error {
	r.unlocks = append(r.unlocks, unlock)
	return nil
}

type heldFixture struct {
	mgr       *hitl.Manager
	ctx       context.Context
	sessionID string
	key       ed25519.PrivateKey
	unlocks   *presence.Unlocks
	recorded  *recordedUnlocks
}

func newHeldFixture(t *testing.T) heldFixture {
	t.Helper()
	sqlDB, mgr, sessionID := newTestManager(t)
	insertSession(t, sqlDB, sessionID)
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	testutil.FailErr(t, "generate presence key", err)
	broker := presence.NewBroker()
	testutil.FailErr(t, "configure presence key", broker.Configure(base64.RawURLEncoding.EncodeToString(publicKey)))
	unlocks, recorded := presence.NewUnlocks(), &recordedUnlocks{}
	mgr.SetVaultUnlock(broker, unlocks, recorded)
	mgr.SetApprovalAuthorityInstaller(noopApprovalInstaller{})
	return heldFixture{
		mgr: mgr, ctx: testdbseed.OwnerCaller(t, context.Background(), sqlDB), sessionID: sessionID,
		key: privateKey, unlocks: unlocks, recorded: recorded,
	}
}

var heldRecipients = []secretmatch.Recipient{{
	ID: "https://api.example.com:443", Label: "https://api.example.com", Surface: secretmatch.SurfaceHTTPRequest,
	Kind: secretmatch.DestinationService,
}}

func (f heldFixture) held() *hitl.HeldRelease {
	return &hitl.HeldRelease{
		ProjectID: testdbseed.DefaultProjectID, ChatSessionID: f.sessionID,
		Secrets:    []hitl.HeldSecret{{SecretID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", Version: 1, Name: "Deploy key"}},
		Recipients: heldRecipients,
	}
}

// requestHeld raises a card that would hand a person's value to a service.
func (f heldFixture) requestHeld(t *testing.T) string {
	t.Helper()
	resp, err := requestSecretApprovalCheckpoint(t, f.ctx, f.mgr, hitl.CheckpointRequest{
		SessionID: f.sessionID, Kind: api.CheckpointKindToolApproval,
		ProposedAction: &hitl.ProposedAction{Tool: "http_request", SessionID: f.sessionID},
		SecretScreen: &hitl.SecretScreen{
			Surface: "http_request", SurfaceLabel: "HTTP request", CanRedact: true, Managed: true,
			DestinationID: heldRecipients[0].ID, DestinationLabel: heldRecipients[0].Label, Recipients: heldRecipients,
			RuleID: secretmatch.ManagedRuleID, RuleTitle: secretmatch.ManagedRuleTitle, GenericShape: "Protected value",
			Occurrences: 1, SourceKind: "tool_argument", OriginKind: "field", Held: f.held(),
		},
	})
	testutil.FailErr(t, "request held send", err)
	return resp.CheckpointID
}

// requestUnlock raises the card that only unlocks the chat.
func (f heldFixture) requestUnlock(t *testing.T) string {
	t.Helper()
	action := hitl.ProposedAction{Tool: "http_request", SessionID: f.sessionID}
	plan, err := hitl.NewUnlockPlan(action, *f.held(), &hitl.SecretScreen{
		Surface: "http_request", SurfaceLabel: "HTTP request", CanRedact: true, Managed: true, Recipients: heldRecipients,
	})
	testutil.FailErr(t, "build unlock plan", err)
	resp, err := f.mgr.RequestCheckpoint(f.ctx, hitl.CheckpointRequest{
		SessionID: f.sessionID, Kind: api.CheckpointKindToolApproval, Title: hitl.TitleUnlockForThisChat,
		ProposedAction: &action, ApprovalPlan: plan,
	})
	testutil.FailErr(t, "request unlock", err)
	return resp.CheckpointID
}

func (f heldFixture) proof(t *testing.T, challenge hitl.UnlockChallenge) presence.Proof {
	t.Helper()
	signature := ed25519.Sign(f.key, presence.SigningMessage(challenge.ProofPayload, presence.AuthenticatorMacOS))
	return presence.Proof{
		ChallengeID: challenge.ID, Authenticator: presence.AuthenticatorMacOS,
		Signature: base64.RawURLEncoding.EncodeToString(signature),
	}
}

// approveWithPresence answers option the way the desktop shell does.
func (f heldFixture) approveWithPresence(t *testing.T, checkpointID, optionID string) (*hitl.CheckpointResponse, hitl.UnlockChallenge) {
	t.Helper()
	challenge, err := f.mgr.BeginUnlockChallenge(f.ctx, f.sessionID, checkpointID, optionID, "main")
	testutil.FailErr(t, "begin unlock challenge", err)
	final, err := f.mgr.ResolveApprovalOptionBy(f.ctx, f.sessionID, checkpointID, optionID, hitl.AttestedApproval(f.proof(t, challenge)))
	testutil.FailErr(t, "approve with presence", err)
	return final, challenge
}

func TestHeldPlanOffersNoChoiceThatAnswersWithoutThePerson(t *testing.T) {
	f := newHeldFixture(t)
	f.requestHeld(t)
	pending, err := f.mgr.ListPending(f.ctx, f.sessionID, nil)
	testutil.FailErr(t, "list pending", err)
	plan := pending[0].ToolApproval.Plan
	if plan.HeldRelease == nil || plan.HeldRelease.Secrets[0].Name != "Deploy key" || len(plan.HeldRelease.Recipients) != 1 {
		t.Fatalf("held release = %+v", plan.HeldRelease)
	}
	for _, option := range plan.Options {
		if option.Kind == api.ApprovalOptionKindQuiet {
			t.Fatalf("a held send offers quiet option %s", option.ID)
		}
	}
}

func TestLockedChatRefusesAnApprovalWithoutPresence(t *testing.T) {
	f := newHeldFixture(t)
	checkpointID := f.requestHeld(t)
	if _, err := f.mgr.ResolveApprovalOption(f.ctx, f.sessionID, checkpointID, "send_unchanged"); !errors.Is(err, hitl.ErrPresenceRequired) {
		t.Fatalf("bearer-only approval error = %v", err)
	}
	if len(f.recorded.unlocks) != 0 {
		t.Fatal("a refused approval recorded an unlock")
	}
}

// Presence on a locked chat's approval unlocks the chat once the approval
// commits, and the audit row lands in that commit.
func TestPresenceOnAnApprovalUnlocksTheChat(t *testing.T) {
	f := newHeldFixture(t)
	final, challenge := f.approveWithPresence(t, f.requestHeld(t), "send_unchanged")
	if final.Status != hitl.DecisionStatusApproved {
		t.Fatalf("final = %+v", final)
	}
	if len(f.recorded.unlocks) != 1 || f.recorded.unlocks[0].ID != challenge.ID {
		t.Fatalf("recorded unlocks = %+v", f.recorded.unlocks)
	}
	if unlock, open := f.unlocks.Active(f.sessionID); !open || unlock.ID != challenge.ID {
		t.Fatalf("chat unlock = %+v, %v", unlock, open)
	}
}

// While the chat is unlocked, a new recipient's card is an ordinary choice.
func TestUnlockedChatApprovesWithoutPresence(t *testing.T) {
	f := newHeldFixture(t)
	f.approveWithPresence(t, f.requestHeld(t), "send_unchanged")
	next := f.requestHeld(t)
	if _, err := f.mgr.BeginUnlockChallenge(f.ctx, f.sessionID, next, "send_unchanged", "main"); !errors.Is(err, hitl.ErrPresenceNotRequired) {
		t.Fatalf("unlocked challenge error = %v", err)
	}
	final, err := f.mgr.ResolveApprovalOption(f.ctx, f.sessionID, next, "send_unchanged")
	testutil.FailErr(t, "unlocked approval", err)
	if final.Status != hitl.DecisionStatusApproved || len(f.recorded.unlocks) != 1 {
		t.Fatalf("final = %+v, unlocks = %d", final, len(f.recorded.unlocks))
	}
}

// The unlock card's one choice needs presence and opens the chat.
func TestUnlockCardOpensTheChat(t *testing.T) {
	f := newHeldFixture(t)
	checkpointID := f.requestUnlock(t)
	if _, err := f.mgr.ResolveApprovalOption(f.ctx, f.sessionID, checkpointID, "unlock_for_chat"); !errors.Is(err, hitl.ErrPresenceRequired) {
		t.Fatalf("bearer-only unlock error = %v", err)
	}
	f.approveWithPresence(t, checkpointID, "unlock_for_chat")
	if _, open := f.unlocks.Active(f.sessionID); !open {
		t.Fatal("the unlock card left the chat locked")
	}
}

// A proof answers the exact card and option it was issued for.
func TestUnlockProofCannotAnswerAnotherCard(t *testing.T) {
	f := newHeldFixture(t)
	first := f.requestHeld(t)
	challenge, err := f.mgr.BeginUnlockChallenge(f.ctx, f.sessionID, first, "send_unchanged", "main")
	testutil.FailErr(t, "begin unlock challenge", err)
	second := f.requestHeld(t)
	if second == first {
		t.Fatal("two requests shared one card")
	}
	if _, err := f.mgr.ResolveApprovalOptionBy(f.ctx, f.sessionID, second, "send_unchanged", hitl.AttestedApproval(f.proof(t, challenge))); !errors.Is(err, presence.ErrDenied) {
		t.Fatalf("cross-card proof error = %v", err)
	}
	if _, open := f.unlocks.Active(f.sessionID); open {
		t.Fatal("a refused proof unlocked the chat")
	}
}

// A redacted send strips the value, so it sends nothing held and needs no presence.
func TestRedactedSendNeedsNoPresence(t *testing.T) {
	f := newHeldFixture(t)
	checkpointID := f.requestHeld(t)
	if _, err := f.mgr.BeginUnlockChallenge(f.ctx, f.sessionID, checkpointID, "send_redacted", "main"); !errors.Is(err, hitl.ErrPresenceNotRequired) {
		t.Fatalf("redacted challenge error = %v", err)
	}
	final, err := f.mgr.ResolveApprovalOption(f.ctx, f.sessionID, checkpointID, "send_redacted")
	testutil.FailErr(t, "redacted send", err)
	if !final.Result.RedactSecrets {
		t.Fatalf("redacted final = %+v", final.Result)
	}
	if _, open := f.unlocks.Active(f.sessionID); open {
		t.Fatal("a redacted send unlocked the chat")
	}
}

// Confirming one card unlocks the chat and answers its open unlock-only
// card in the same moment, so the person never confirms the same thing twice.
func TestPresenceAnswersTheChatsOpenUnlockCard(t *testing.T) {
	f := newHeldFixture(t)
	unlockCard := f.requestUnlock(t)
	f.approveWithPresence(t, f.requestHeld(t), "send_unchanged")
	settled, err := f.mgr.PollCheckpoint(f.ctx, unlockCard)
	testutil.FailErr(t, "poll unlock card", err)
	if !hitl.CheckpointAuthorizes(settled) || settled.Result.OptionID != "unlock_for_chat" {
		t.Fatalf("unlock card = %+v", settled)
	}
}

// A card that also approves a recipient keeps its own question.
func TestUnlockingLeavesOtherRecipientsCardsOpen(t *testing.T) {
	f := newHeldFixture(t)
	other := f.requestHeld(t)
	f.approveWithPresence(t, f.requestUnlock(t), "unlock_for_chat")
	pending, err := f.mgr.PollCheckpoint(f.ctx, other)
	testutil.FailErr(t, "poll recipient card", err)
	if pending.Status != hitl.DecisionStatusPending {
		t.Fatalf("recipient card = %+v", pending)
	}
}
