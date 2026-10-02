package hitl_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/credentialstore"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/presence"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type recordedReleases struct{ releases []hitl.AttestedRelease }

func (r *recordedReleases) RecordReleaseTx(_ context.Context, _ *sql.Tx, release hitl.AttestedRelease) error {
	r.releases = append(r.releases, release)
	return nil
}

type heldFixture struct {
	mgr       *hitl.Manager
	ctx       context.Context
	sessionID string
	key       ed25519.PrivateKey
	recorded  *recordedReleases
}

func newHeldFixture(t *testing.T) heldFixture {
	t.Helper()
	sqlDB, mgr, sessionID := newTestManager(t)
	insertSession(t, sqlDB, sessionID)
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	testutil.FailErr(t, "generate presence key", err)
	broker := presence.NewBroker()
	testutil.FailErr(t, "configure presence key", broker.Configure(base64.RawURLEncoding.EncodeToString(publicKey)))
	store, err := credentialstore.OpenFile(credentialstore.Slot{
		Path: filepath.Join(t.TempDir(), credentialstore.VaultBasename), Namespace: credentialstore.NamespacePresenceReleases,
		Context: "presence release ledger",
	}, func(id string) bool { return id != "" })
	testutil.FailErr(t, "open ledger", err)
	recorded := &recordedReleases{}
	mgr.SetHeldRelease(broker, presence.NewReleaseLedger(store), recorded)
	mgr.SetApprovalAuthorityInstaller(noopApprovalInstaller{})
	return heldFixture{
		mgr: mgr, ctx: testdbseed.OwnerCaller(t, context.Background(), sqlDB), sessionID: sessionID,
		key: privateKey, recorded: recorded,
	}
}

// requestHeld raises a card that would hand a person's value to a service.
func (f heldFixture) requestHeld(t *testing.T) string {
	t.Helper()
	recipients := []secretmatch.Recipient{{
		ID: "https://api.example.com:443", Label: "https://api.example.com", Surface: secretmatch.SurfaceHTTPRequest,
		Kind: secretmatch.DestinationService,
	}}
	resp, err := requestSecretApprovalCheckpoint(t, f.ctx, f.mgr, hitl.CheckpointRequest{
		SessionID: f.sessionID, Kind: api.CheckpointKindToolApproval,
		ProposedAction: &hitl.ProposedAction{Tool: "http_request", SessionID: f.sessionID},
		SecretScreen: &hitl.SecretScreen{
			Surface: "http_request", SurfaceLabel: "HTTP request", CanRedact: true, Managed: true,
			DestinationID: recipients[0].ID, DestinationLabel: recipients[0].Label, Recipients: recipients,
			RuleID: secretmatch.ManagedRuleID, RuleTitle: secretmatch.ManagedRuleTitle, GenericShape: "Protected value",
			Occurrences: 1, SourceKind: "tool_argument", OriginKind: "field",
			Held: &hitl.HeldRelease{
				Secrets:      []hitl.HeldSecret{{SecretID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", Version: 1, Name: "Deploy key"}},
				Fingerprints: []string{"sf1_held"}, Recipients: recipients,
			},
		},
	})
	testutil.FailErr(t, "request held release", err)
	return resp.CheckpointID
}

func (f heldFixture) proof(t *testing.T, challenge hitl.ReleaseChallenge) presence.Proof {
	t.Helper()
	signature := ed25519.Sign(f.key, presence.SigningMessage(challenge.ProofPayload, presence.AuthenticatorMacOS))
	return presence.Proof{
		ChallengeID: challenge.ID, Authenticator: presence.AuthenticatorMacOS,
		Signature: base64.RawURLEncoding.EncodeToString(signature),
	}
}

func TestHeldReleasePlanAsksOnlyThePersonsPresence(t *testing.T) {
	f := newHeldFixture(t)
	f.requestHeld(t)
	pending, err := f.mgr.ListPending(f.ctx, f.sessionID, nil)
	testutil.FailErr(t, "list pending", err)
	plan := pending[0].ToolApproval.Plan
	if plan.HeldRelease == nil || plan.HeldRelease.Secrets[0].Name != "Deploy key" || len(plan.HeldRelease.Recipients) != 1 {
		t.Fatalf("held release = %+v", plan.HeldRelease)
	}
	for _, option := range plan.Options {
		if option.Kind == api.ApprovalOptionKindQuiet || option.Rung == api.ApprovalOptionRungDay {
			t.Fatalf("a held release offers %s/%s, which could answer without the person", option.Kind, option.Rung)
		}
	}
}

func TestHeldReleaseRefusesAnAnswerWithoutPresence(t *testing.T) {
	f := newHeldFixture(t)
	checkpointID := f.requestHeld(t)
	if _, err := f.mgr.ResolveApprovalOption(f.ctx, f.sessionID, checkpointID, "send_unchanged"); !errors.Is(err, hitl.ErrPresenceRequired) {
		t.Fatalf("bearer-only approval error = %v", err)
	}
	if len(f.recorded.releases) != 0 {
		t.Fatal("a refused approval recorded a release")
	}
}

func TestHeldReleaseRecordsTheAttestedAnswer(t *testing.T) {
	f := newHeldFixture(t)
	checkpointID := f.requestHeld(t)
	challenge, err := f.mgr.BeginReleaseChallenge(f.ctx, f.sessionID, checkpointID, "send_unchanged", "main")
	testutil.FailErr(t, "begin release challenge", err)
	if challenge.Prompt == "" {
		t.Fatal("release challenge carries no prompt")
	}
	final, err := f.mgr.ResolveApprovalOptionBy(f.ctx, f.sessionID, checkpointID, "send_unchanged", hitl.AttestedApproval(f.proof(t, challenge)))
	testutil.FailErr(t, "attested approval", err)
	if final.Status != hitl.DecisionStatusApproved || final.Result.AttestationID != challenge.ID {
		t.Fatalf("attested final = %+v", final.Result)
	}
	if len(f.recorded.releases) != 1 || f.recorded.releases[0].Scope != "once" ||
		f.recorded.releases[0].Attestation.ID != challenge.ID || f.recorded.releases[0].CheckpointID != checkpointID {
		t.Fatalf("recorded releases = %+v", f.recorded.releases)
	}
}

// A proof answers the exact card and option it was issued for.
func TestHeldReleaseProofCannotAnswerAnotherCard(t *testing.T) {
	f := newHeldFixture(t)
	first := f.requestHeld(t)
	challenge, err := f.mgr.BeginReleaseChallenge(f.ctx, f.sessionID, first, "send_unchanged", "main")
	testutil.FailErr(t, "begin release challenge", err)
	second := f.requestHeld(t)
	if second == first {
		t.Fatal("two requests shared one card")
	}
	if _, err := f.mgr.ResolveApprovalOptionBy(f.ctx, f.sessionID, second, "send_unchanged", hitl.AttestedApproval(f.proof(t, challenge))); !errors.Is(err, presence.ErrDenied) {
		t.Fatalf("cross-card proof error = %v", err)
	}
}

// A redacted send strips the value, so it releases nothing and needs no presence.
func TestHeldReleaseRedactedSendNeedsNoPresence(t *testing.T) {
	f := newHeldFixture(t)
	checkpointID := f.requestHeld(t)
	if _, err := f.mgr.BeginReleaseChallenge(f.ctx, f.sessionID, checkpointID, "send_redacted", "main"); !errors.Is(err, hitl.ErrPresenceNotRequired) {
		t.Fatalf("redacted challenge error = %v", err)
	}
	final, err := f.mgr.ResolveApprovalOption(f.ctx, f.sessionID, checkpointID, "send_redacted")
	testutil.FailErr(t, "redacted send", err)
	if !final.Result.RedactSecrets || final.Result.AttestationID != "" {
		t.Fatalf("redacted final = %+v", final.Result)
	}
}
