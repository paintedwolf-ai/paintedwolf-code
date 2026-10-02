package settings

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/credentialstore"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/presence"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestSecretFingerprintGrantsComposeWithinOneProject(t *testing.T) {
	store, err := NewApprovalStoreAt(filepath.Join(t.TempDir(), "approvals.yaml"))
	testutil.FailErr(t, "open approval store", err)
	gate := NewRuleApprovalGate(store, NoSources()).(*RuleApprovalGate)
	project := filepath.Join(t.TempDir(), "project")
	one := secretmatch.SecretFingerprint("sf1_one")
	two := secretmatch.SecretFingerprint("sf1_two")

	putSecretGrant(t, store, "grant-one", "project-id", project, []secretmatch.SecretFingerprint{one})
	putSecretGrant(t, store, "grant-two", "project-id", project, []secretmatch.SecretFingerprint{two})
	if !releaseCovered(gate, "", "project-id", "provider", "model_request", []string{string(two), string(one)}) {
		t.Fatal("active grants did not compose over the exact fingerprint set")
	}
	if releaseCovered(gate, "", "project-id", "provider", "model_request", []string{string(one), "sf1_three"}) {
		t.Fatal("a partial fingerprint set was treated as covered")
	}
	if releaseCovered(gate, "", "other-project", "provider", "model_request", []string{string(one)}) {
		t.Fatal("secret authority crossed the reviewed project")
	}
	if releaseCovered(gate, "", "", "provider", "model_request", []string{string(one)}) {
		t.Fatal("secret authority fell back to a non-canonical project identity")
	}
	if releaseCovered(gate, "", "project-id", "other-provider", "model_request", []string{string(one)}) {
		t.Fatal("secret authority crossed the reviewed destination")
	}
	if releaseCovered(gate, "", "project-id", "provider", "mcp", []string{string(one)}) {
		t.Fatal("secret authority crossed the reviewed outbound surface")
	}
}

func TestChatSecretGrantCoversOnlyItsChat(t *testing.T) {
	store, err := NewApprovalStoreAt(filepath.Join(t.TempDir(), "approvals.yaml"))
	testutil.FailErr(t, "open approval store", err)
	gate := NewRuleApprovalGate(store, NoSources()).(*RuleApprovalGate)
	fingerprints := []secretmatch.SecretFingerprint{"sf1_one"}
	created, err := gate.ApplyGrant(hitl.ApprovalGrant{
		ID: "grant-chat", Scope: hitl.ApprovalGrantScopeChat,
		Predicate: hitl.ApprovalGrantPredicate{
			Category: hitl.ApprovalGrantCategorySecret,
			Pattern:  secretmatch.FingerprintDigest(fingerprints),
		},
		ChatSessionID: "chat-a", ProjectID: "project-id", SecretRecipients: testSecretRecipients(),
		Witness:            hitl.SecretReleaseWitness(testSecretRecipients()),
		SecretFingerprints: []string{"sf1_one"},
	})
	testutil.FailErr(t, "apply chat secret grant", err)
	if !created {
		t.Fatal("chat secret grant was not created")
	}
	if !releaseCovered(gate, "chat-a", "project-id", "provider", "model_request", []string{"sf1_one"}) {
		t.Fatal("chat secret grant did not cover its chat")
	}
	if releaseCovered(gate, "chat-b", "project-id", "provider", "model_request", []string{"sf1_one"}) {
		t.Fatal("chat secret grant crossed chats")
	}
	expired := time.Now().UTC().Add(-time.Minute)
	_, err = gate.ApplyGrant(hitl.ApprovalGrant{
		ID: "grant-expired", Scope: hitl.ApprovalGrantScopeChat,
		Predicate:     hitl.ApprovalGrantPredicate{Category: hitl.ApprovalGrantCategorySecret, Pattern: secretmatch.FingerprintDigest([]secretmatch.SecretFingerprint{"sf1_expired"})},
		ChatSessionID: "chat-a", ProjectID: "project-id", ExpiresAt: &expired,
		SecretRecipients: testSecretRecipients(), Witness: hitl.SecretReleaseWitness(testSecretRecipients()),
		SecretFingerprints: []string{"sf1_expired"},
	})
	testutil.FailErr(t, "apply expired chat secret grant", err)
	if releaseCovered(gate, "chat-a", "project-id", "provider", "model_request", []string{"sf1_expired"}) {
		t.Fatal("expired chat secret grant was treated as active")
	}
}

func TestSecretGrantRejectsPatternDrift(t *testing.T) {
	expires := time.Now().UTC().Add(time.Hour)
	err := validateApprovalConfig(ApprovalConfig{Grants: []ApprovalGrant{{
		ID: "grant-drift", Scope: hitl.ApprovalGrantScopeProject,
		Category: ApprovalCategorySecret, Pattern: "wrong", ProjectID: "project-id", ProjectDir: "/project",
		Title: "Allow for 1 hour", ExpiresAt: &expires,
		SecretFingerprints: []string{"sf1_one"},
	}}})
	if err == nil {
		t.Fatal("fingerprint-set digest drift was accepted")
	}
}

func TestSecretGrantRequiresCanonicalProjectID(t *testing.T) {
	expires := time.Now().UTC().Add(time.Hour)
	fingerprints := []secretmatch.SecretFingerprint{"sf1_one"}
	err := validateApprovalConfig(ApprovalConfig{Grants: []ApprovalGrant{{
		ID: "grant-no-project", Scope: hitl.ApprovalGrantScopeProject,
		Category: ApprovalCategorySecret, Pattern: secretmatch.FingerprintDigest(fingerprints), ProjectDir: "/project",
		Title: "Allow for 1 hour", ExpiresAt: &expires,
		SecretFingerprints: []string{"sf1_one"},
	}}})
	if err == nil {
		t.Fatal("secret grant without canonical project identity was accepted")
	}
}

func TestSecretGrantRejectsNonCanonicalFingerprint(t *testing.T) {
	expires := time.Now().UTC().Add(time.Hour)
	err := validateApprovalConfig(ApprovalConfig{Grants: []ApprovalGrant{{
		ID: "grant-bad-fingerprint", Scope: hitl.ApprovalGrantScopeProject,
		Category: ApprovalCategorySecret, ProjectID: "project-id", ProjectDir: "/project", Title: "Allow for 1 hour", ExpiresAt: &expires,
		SecretFingerprints: []string{"sf1_one", " "},
	}}})
	if err == nil {
		t.Fatal("secret grant with non-canonical fingerprint was accepted")
	}
}

func putSecretGrant(t *testing.T, store *ApprovalStore, id, projectID, project string, fingerprints []secretmatch.SecretFingerprint) {
	t.Helper()
	expires := time.Now().UTC().Add(time.Hour)
	values := make([]string, 0, len(fingerprints))
	for _, fingerprint := range fingerprints {
		values = append(values, string(fingerprint))
	}
	_, err := store.UpsertGlobalGrant(ApprovalGrant{
		ID: id, Scope: hitl.ApprovalGrantScopeProject,
		Category: ApprovalCategorySecret, Pattern: secretmatch.FingerprintDigest(fingerprints), ProjectID: projectID, ProjectDir: project,
		Title: "Allow for 1 hour", Coverage: "detected secret", GrantedAt: time.Now().UTC(), ExpiresAt: &expires,
		ExpiresWhen: "in 1 hour or when revoked", ReaskWhen: "a different secret or project is involved",
		SecretRecipients: testSecretRecipients(), Witness: hitl.SecretReleaseWitness(testSecretRecipients()),
		SecretFingerprints: values,
		GrantedByPersonID:  testutil.HostOwner().ID,
	})
	testutil.FailErr(t, "store secret grant", err)
}

func testSecretRecipients() []secretmatch.Recipient {
	return []secretmatch.Recipient{{ID: "provider", Label: "Provider", Surface: secretmatch.SurfaceModel, Kind: secretmatch.DestinationModelProvider}}
}

func TestMultiRecipientPermissionRevocationAndIsolation(t *testing.T) {
	store, err := NewApprovalStoreAt(filepath.Join(t.TempDir(), "approvals.yaml"))
	testutil.FailErr(t, "open store", err)
	gate := NewRuleApprovalGate(store, NoSources()).(*RuleApprovalGate)
	recipients := []secretmatch.Recipient{
		{ID: "process", Label: "Processes in this chat", Surface: secretmatch.SurfaceCommand, Kind: secretmatch.DestinationProcess},
		{ID: "service", Label: "http://localhost:8080", Surface: secretmatch.SurfaceHTTPRequest, Kind: secretmatch.DestinationService},
	}
	grant := hitl.ApprovalGrant{ID: "multi", Scope: hitl.ApprovalGrantScopeChat, ChatSessionID: "chat", ProjectID: "project",
		Predicate:          hitl.ApprovalGrantPredicate{Category: hitl.ApprovalGrantCategorySecret, Pattern: secretmatch.FingerprintDigest([]secretmatch.SecretFingerprint{"one"})},
		SecretFingerprints: []string{"one"}, SecretRecipients: recipients, Witness: hitl.SecretReleaseWitness(recipients)}
	_, err = gate.ApplyGrant(grant)
	testutil.FailErr(t, "apply recipient set", err)
	grant.SecretRecipients[0].ID = "changed"
	grant.SecretFingerprints[0] = "changed"
	for _, receiver := range []struct{ id, surface string }{{"process", "command"}, {"service", "http_request"}} {
		if !releaseCovered(gate, "chat", "project", receiver.id, receiver.surface, []string{"one"}) {
			t.Fatal("reviewed receiver was not covered")
		}
		for _, scope := range []struct{ chat, project string }{{"other", "project"}, {"chat", "other"}} {
			if releaseCovered(gate, scope.chat, scope.project, receiver.id, receiver.surface, []string{"one"}) {
				t.Fatal("permission escaped chat/project")
			}
		}
	}
	if releaseCovered(gate, "chat", "project", "service", "http_request", []string{"rotated"}) {
		t.Fatal("rotation reused the old value's permission")
	}
	listed := gate.ListGrants("chat")
	for i := range listed {
		if len(listed[i].SecretRecipients) > 0 {
			listed[i].SecretRecipients[0].ID = "tampered"
		}
	}
	if !releaseCovered(gate, "chat", "project", "service", "http_request", []string{"one"}) {
		t.Fatal("listing mutated stored permission")
	}
	removed, err := gate.RevokeGrant("multi")
	testutil.FailErr(t, "revoke permission", err)
	if !removed || releaseCovered(gate, "chat", "project", "service", "http_request", []string{"one"}) {
		t.Fatal("revoked permission remained live")
	}
}

func TestOneLocalSecretLeaseCoversCommandLoopbackHTTPAndChatProcesses(t *testing.T) {
	store, err := NewApprovalStoreAt(filepath.Join(t.TempDir(), "approvals.yaml"))
	testutil.FailErr(t, "open approval store", err)
	gate := NewRuleApprovalGate(store, NoSources()).(*RuleApprovalGate)

	localRecipients := []secretmatch.Recipient{secretmatch.LocalRecipient}
	grant := hitl.ApprovalGrant{
		ID:                 "local-secret-grant",
		Scope:              hitl.ApprovalGrantScopeChat,
		ChatSessionID:      "chat-local",
		ProjectID:          "proj-local",
		Predicate:          hitl.ApprovalGrantPredicate{Category: hitl.ApprovalGrantCategorySecret, Pattern: secretmatch.FingerprintDigest([]secretmatch.SecretFingerprint{"secret123"})},
		SecretFingerprints: []string{"secret123"},
		SecretRecipients:   localRecipients,
		Witness:            hitl.SecretReleaseWitness(localRecipients),
	}
	_, err = gate.ApplyGrant(grant)
	testutil.FailErr(t, "apply local secret grant", err)

	if !releaseCovered(gate, "chat-local", "proj-local", "local", "command", []string{"secret123"}) {
		t.Fatal("local secret lease must cover command execution")
	}
	if !releaseCovered(gate, "chat-local", "proj-local", "127.0.0.1:8080", "http_request", []string{"secret123"}) {
		t.Fatal("local secret lease must cover loopback HTTP (127.0.0.1:8080)")
	}
	if !releaseCovered(gate, "chat-local", "proj-local", "localhost:3000", "http_request", []string{"secret123"}) {
		t.Fatal("local secret lease must cover loopback HTTP (localhost:3000)")
	}
	if !releaseCovered(gate, "chat-local", "proj-local", "local", "terminal", []string{"secret123"}) {
		t.Fatal("local secret lease must cover the chat's terminal processes")
	}
	if releaseCovered(gate, "chat-local", "proj-local", "api.example.com", "http_request", []string{"secret123"}) {
		t.Fatal("local secret lease must not cover an external host")
	}
}


// releaseCovered asks about values no person holds.
func releaseCovered(gate *RuleApprovalGate, chat, project, destination, surface string, fingerprints []string) bool {
	covered, _ := gate.SecretReleaseCovered(chat, project, destination, surface, fingerprints, nil)
	return covered
}

func heldReleaseGrant(attestation *presence.Attestation) hitl.ApprovalGrant {
	expires := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
	return hitl.ApprovalGrant{
		ID: "held-grant", Scope: hitl.ApprovalGrantScopeProject, ProjectID: "project-id", ProjectDir: "/project",
		Predicate: hitl.ApprovalGrantPredicate{
			Category: hitl.ApprovalGrantCategorySecret, Pattern: secretmatch.FingerprintDigest([]secretmatch.SecretFingerprint{"sf1_held"}),
		},
		Title: "Allow for this project", ExpiresAt: &expires, GrantedAt: time.Now().UTC(),
		SecretRecipients: testSecretRecipients(), Witness: hitl.SecretReleaseWitness(testSecretRecipients()),
		SecretFingerprints: []string{"sf1_held"}, GrantedByPersonID: testutil.HostOwner().ID, Attestation: attestation,
	}
}

func testReleaseLedger(t *testing.T) *presence.ReleaseLedger {
	t.Helper()
	store, err := credentialstore.OpenFile(credentialstore.Slot{
		Path: filepath.Join(t.TempDir(), credentialstore.VaultBasename), Namespace: credentialstore.NamespacePresenceReleases,
		Context: "presence release ledger",
	}, func(id string) bool { return id != "" })
	testutil.FailErr(t, "open ledger store", err)
	return presence.NewReleaseLedger(store)
}

// A grant covers a person-held value only while the vault ledger lists it:
// a grant written into ordinary storage, or restored after revocation, does not.
func TestHeldValueCoverageNeedsTheLedger(t *testing.T) {
	store, err := NewApprovalStoreAt(filepath.Join(t.TempDir(), "approvals.yaml"))
	testutil.FailErr(t, "open approval store", err)
	ledger := testReleaseLedger(t)
	sources := NoSources()
	sources.ReleaseLedger = ledger
	gate := NewRuleApprovalGate(store, sources).(*RuleApprovalGate)
	attestation := &presence.Attestation{
		ID: "11111111-1111-4111-8111-111111111111", PersonID: testutil.HostOwner().ID,
		Authenticator: presence.AuthenticatorMacOS, AttestedAt: time.Now().UTC().Truncate(time.Second),
	}
	grant := heldReleaseGrant(attestation)
	_, err = gate.ApplyGrant(grant)
	testutil.FailErr(t, "apply held grant", err)
	held := []string{"sf1_held"}
	if covered, _ := gate.SecretReleaseCovered("", "project-id", "provider", "model_request", held, held); covered {
		t.Fatal("a grant the ledger does not list covered a held value")
	}
	if !releaseCovered(gate, "", "project-id", "provider", "model_request", held) {
		t.Fatal("the same grant stopped covering values no person holds")
	}

	testutil.FailErr(t, "record ledger entry", ledger.Record(grant.ReleaseCoverage(), *attestation))
	covered, attestations := gate.SecretReleaseCovered("", "project-id", "provider", "model_request", held, held)
	if !covered || attestations["sf1_held"] != attestation.ID {
		t.Fatalf("ledgered grant coverage = %v %v", covered, attestations)
	}

	removed, err := gate.RevokeGrant("held-grant")
	testutil.FailErr(t, "revoke held grant", err)
	if !removed {
		t.Fatal("held grant was not found")
	}
	_, err = gate.ApplyGrant(grant)
	testutil.FailErr(t, "restore a stored copy", err)
	if covered, _ := gate.SecretReleaseCovered("", "project-id", "provider", "model_request", held, held); covered {
		t.Fatal("a revoked grant restored from a stored copy covered a held value")
	}
}
