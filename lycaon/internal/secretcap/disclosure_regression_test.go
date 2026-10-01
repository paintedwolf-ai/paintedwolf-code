package secretcap

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
)

// Equal values retain independent capability identities and request metadata.
func TestPutMintsSeparatelyForMatchingBytes(t *testing.T) {
	service, _, _ := testService(t)
	first, err := service.CreateSettingsSecret(t.Context(), CreateSettingsSecretRequest{
		ProjectID: testdbseed.DefaultProjectID, PersonID: testOwner(t, service), OperationID: "dedupe-1",
		Name: "Deploy key", Purpose: "publish the built image", Value: "shared-entered-value-01",
	})
	testutil.FailErr(t, "create first settings secret", err)
	second, err := service.CreateSettingsSecret(t.Context(), CreateSettingsSecretRequest{
		ProjectID: testdbseed.DefaultProjectID, PersonID: testOwner(t, service), OperationID: "dedupe-2",
		Name: "Deploy key again", Purpose: "same bytes under another name",
		Value: "shared-entered-value-01", AgentUseEndsAt: "2026-09-30T12:00:00Z",
	})
	testutil.FailErr(t, "create second settings secret", err)
	if second.Reference == first.Reference {
		t.Fatalf("matching bytes returned the existing capability %q", first.Reference)
	}
	if second.Name != "Deploy key again" || second.Purpose != "same bytes under another name" {
		t.Fatalf("declared labels were discarded: %+v", second)
	}
	if second.AgentUseEndsAt == nil || *second.AgentUseEndsAt != "2026-09-30T12:00:00Z" {
		t.Fatalf("declared agent-use deadline was discarded: %+v", second.AgentUseEndsAt)
	}
	listed, err := service.ListProject(t.Context(), testdbseed.DefaultProjectID)
	testutil.FailErr(t, "list project secrets", err)
	if len(listed) != 2 {
		t.Fatalf("project holds %d capabilities, want 2", len(listed))
	}
}

// Replacement responses do not disclose value equality.
func TestReplaceValueTellsNothingApartByValue(t *testing.T) {
	service, _, _ := testService(t)
	meta, err := service.CreateSettingsSecret(t.Context(), CreateSettingsSecretRequest{
		ProjectID: testdbseed.DefaultProjectID, PersonID: testOwner(t, service), OperationID: "replace-no-oracle",
		Name: "Rotating", Purpose: "value under guess", Value: "current-stored-value-1",
	})
	testutil.FailErr(t, "create settings secret", err)

	same, err := service.ReplaceValue(t.Context(), ReplaceValueRequest{
		ProjectID: testdbseed.DefaultProjectID, Reference: meta.Reference, Value: "current-stored-value-1",
	})
	testutil.FailErr(t, "replace with matching bytes", err)
	if same.Version != 2 {
		t.Fatalf("matching bytes did not store a new version: %+v", same)
	}
	different, err := service.ReplaceValue(t.Context(), ReplaceValueRequest{
		ProjectID: testdbseed.DefaultProjectID, Reference: meta.Reference, Value: "current-stored-value-2",
	})
	testutil.FailErr(t, "replace with different bytes", err)
	if different.Version != 3 || different.State != same.State {
		t.Fatalf("matching and differing replacements are distinguishable: %+v vs %+v", same, different)
	}
}

// All creation paths apply the screening minimum.
func TestMintRefusesValueBelowTheScreenFloor(t *testing.T) {
	service, _, _ := testService(t)
	short := strings.Repeat("a", secretmatch.MinManagedSecretRunes-1)
	shortRunes := strings.Repeat("é", secretmatch.MinManagedSecretRunes-1)

	for _, value := range []string{"", "x", short, shortRunes} {
		_, err := service.CreateSettingsSecret(t.Context(), CreateSettingsSecretRequest{
			ProjectID: testdbseed.DefaultProjectID, PersonID: testOwner(t, service), OperationID: "floor-settings-" + value,
			Name: "Too short", Purpose: "below the screen floor", Value: value,
		})
		if !errors.Is(err, ErrValueTooShort) || !errors.Is(err, ErrInvalidPut) {
			t.Fatalf("settings mint of %q error = %v", value, err)
		}
	}
	for _, origin := range []string{OriginAskUserResponse, OriginDetected, OriginComposerMarked} {
		_, err := service.Put(t.Context(), PutRequest{
			ProjectID: testdbseed.DefaultProjectID, ChatSessionID: "root-1", SessionID: "root-1",
			OperationID: "floor-" + origin, Name: "Too short", Scope: ScopeChat,
			Origin: origin, PersonID: putPerson(t, service, origin), Value: short,
		})
		if !errors.Is(err, ErrValueTooShort) {
			t.Fatalf("%s mint error = %v", origin, err)
		}
	}
	if _, err := service.Put(t.Context(), PutRequest{
		ProjectID: testdbseed.DefaultProjectID, OperationID: "floor-file-marked",
		Name: "Too short", Purpose: "below the screen floor", Scope: ScopeProject,
		Origin: OriginFileMarked, PersonID: testOwner(t, service), Value: short,
	}); !errors.Is(err, ErrValueTooShort) {
		t.Fatalf("file_marked mint error = %v", err)
	}
	listed, err := service.ListProject(t.Context(), testdbseed.DefaultProjectID)
	testutil.FailErr(t, "list project secrets", err)
	if len(listed) != 0 {
		t.Fatalf("a sub-floor value was minted: %+v", listed)
	}
}

// A value at the floor mints normally; generated material clears it by size.
func TestMintAcceptsValueAtTheScreenFloor(t *testing.T) {
	service, _, _ := testService(t)
	meta, err := service.CreateSettingsSecret(t.Context(), CreateSettingsSecretRequest{
		ProjectID: testdbseed.DefaultProjectID, PersonID: testOwner(t, service), OperationID: "floor-exact",
		Name: "At the floor", Purpose: "exactly the floor length",
		Value: strings.Repeat("é", secretmatch.MinManagedSecretRunes),
	})
	testutil.FailErr(t, "mint at the floor", err)
	if meta.State != StateActive {
		t.Fatalf("floor-length capability state = %+v", meta)
	}
	generated, err := service.Generate(t.Context(), GenerateRequest{
		ProjectID: testdbseed.DefaultProjectID, ChatSessionID: "root-1", SessionID: "root-1",
		OperationID: "floor-generate", Name: "generated", Scope: ScopeChat,
	})
	testutil.FailErr(t, "generate", err)
	if generated.State != StateActive {
		t.Fatalf("generated capability state = %+v", generated)
	}
}

// Revocation is terminal, so only agent-authored provenance is the agent's to end.
func TestAgentRevokeRefusesHumanAuthoredOrigins(t *testing.T) {
	service, _, _ := testService(t)
	for _, origin := range []string{OriginAskUserResponse, OriginComposerMarked} {
		meta, err := service.Put(t.Context(), PutRequest{
			ProjectID: testdbseed.DefaultProjectID, ChatSessionID: "root-1", SessionID: "root-1",
			OperationID: "human-" + origin, Name: "Person's own", Scope: ScopeChat,
			Origin: origin, PersonID: testOwner(t, service), Value: "person-entered-value-1",
		})
		testutil.FailErr(t, "mint "+origin, err)
		if _, err := service.RevokeByAgent(
			t.Context(), testdbseed.DefaultProjectID, "root-1", meta.Metadata.Reference,
		); !errors.Is(err, ErrHumanAuthored) {
			t.Fatalf("agent revoke of %s error = %v", origin, err)
		}
		still, err := service.Describe(t.Context(), testdbseed.DefaultProjectID, "root-1", meta.Metadata.Reference)
		testutil.FailErr(t, "describe after refused revoke", err)
		if still.State != StateActive {
			t.Fatalf("%s state after refused revoke = %q", origin, still.State)
		}
		revoked, err := service.RevokeProject(t.Context(), testdbseed.DefaultProjectID, meta.Metadata.Reference, testOwner(t, service))
		testutil.FailErr(t, "human revoke "+origin, err)
		if revoked.State != StateRevoked {
			t.Fatalf("human revoke of %s left state %q", origin, revoked.State)
		}
	}
	for _, origin := range []string{OriginFileMarked, OriginSettingsEntered} {
		meta, err := service.Put(t.Context(), PutRequest{
			ProjectID: testdbseed.DefaultProjectID, OperationID: "human-" + origin,
			Name: "Person's own", Purpose: "entered by a person", Scope: ScopeProject,
			Origin: origin, PersonID: testOwner(t, service), Value: "person-entered-value-2",
		})
		testutil.FailErr(t, "mint "+origin, err)
		if _, err := service.RevokeByAgent(
			t.Context(), testdbseed.DefaultProjectID, "root-1", meta.Metadata.Reference,
		); !errors.Is(err, ErrHumanAuthored) {
			t.Fatalf("agent revoke of %s error = %v", origin, err)
		}
	}
}

func TestAgentRevokeAllowsAgentAuthoredOrigins(t *testing.T) {
	service, _, _ := testService(t)
	generated, err := service.Generate(t.Context(), GenerateRequest{
		ProjectID: testdbseed.DefaultProjectID, ChatSessionID: "root-1", SessionID: "root-1",
		OperationID: "agent-generated", Name: "generated", Scope: ScopeChat,
	})
	testutil.FailErr(t, "generate", err)
	revoked, err := service.RevokeByAgent(t.Context(), testdbseed.DefaultProjectID, "root-1", generated.Reference)
	testutil.FailErr(t, "agent revoke generated", err)
	if revoked.State != StateRevoked {
		t.Fatalf("generated revoke state = %q", revoked.State)
	}

	detected, err := service.Put(t.Context(), PutRequest{
		ProjectID: testdbseed.DefaultProjectID, ChatSessionID: "root-1", SessionID: "root-1",
		OperationID: "agent-detected", Name: "tracked", Scope: ScopeChat,
		Origin: OriginDetected, Value: "detected-outbound-value-1",
	})
	testutil.FailErr(t, "mint detected", err)
	revokedDetected, err := service.RevokeByAgent(
		t.Context(), testdbseed.DefaultProjectID, "root-1", detected.Metadata.Reference)
	testutil.FailErr(t, "agent revoke detected", err)
	if revokedDetected.State != StateRevoked {
		t.Fatalf("detected revoke state = %q", revokedDetected.State)
	}
}

// Scope decides who may spend a secret, never how long it lives. Deleting its
// chat leaves it active for people while no agent can spend it; promoting it
// hands it to every chat.
func TestChatDeletionLeavesItsSecretsActiveButUnspendable(t *testing.T) {
	service, _, _ := testService(t)
	generated, err := service.Generate(t.Context(), GenerateRequest{
		ProjectID: testdbseed.DefaultProjectID, ChatSessionID: "root-1", SessionID: "root-1",
		OperationID: "outlives-chat", Name: "database password", Scope: ScopeChat,
	})
	testutil.FailErr(t, "generate chat secret", err)
	_, err = service.handle.ExecContext(t.Context(), `UPDATE sessions SET title = ? WHERE id = ?`, "Provision staging", "root-1")
	testutil.FailErr(t, "title the chat", err)
	live, err := service.ListProject(t.Context(), testdbseed.DefaultProjectID)
	testutil.FailErr(t, "list while the chat exists", err)
	if len(live) != 1 || live[0].ChatTitle != "Provision staging" || live[0].ChatDeleted {
		t.Fatalf("secret while its chat exists = %+v", live)
	}
	deleteChat(t, service, "root-1")

	listed, err := service.ListProject(t.Context(), testdbseed.DefaultProjectID)
	testutil.FailErr(t, "list after chat deletion", err)
	if len(listed) != 1 || listed[0].State != StateActive || !listed[0].ChatDeleted || listed[0].ChatTitle != "" {
		t.Fatalf("secret after chat deletion = %+v", listed)
	}
	spend := func() error {
		_, err := service.Resolve(t.Context(), map[string]any{"value": generated.Reference}, ResolveContext{
			ProjectID: testdbseed.DefaultProjectID, ChatSessionID: "root-2", ToolName: "command",
		})
		return err
	}
	if err := spend(); !errors.Is(err, ErrNotVisible) {
		t.Fatalf("another chat spent a deleted chat's secret: %v", err)
	}

	scope, purpose := ScopeProject, "the staging database every chat deploys to"
	promoted, err := service.Update(t.Context(), UpdateRequest{
		ProjectID: testdbseed.DefaultProjectID, Reference: generated.Reference,
		Purpose: &purpose, Scope: &scope,
	})
	testutil.FailErr(t, "promote after chat deletion", err)
	if promoted.ChatDeleted || promoted.ChatSessionID != nil {
		t.Fatalf("promoted metadata = %+v", promoted)
	}
	testutil.FailErr(t, "spend the promoted secret from another chat", spend())
}

func deleteChat(t *testing.T, service *Service, chatSessionID string) {
	t.Helper()
	tx, err := service.handle.BeginTx(t.Context(), nil)
	testutil.FailErr(t, "begin chat deletion", err)
	defer func() { _ = tx.Rollback() }()
	_, err = db.DeleteSessionTree(t.Context(), tx, chatSessionID)
	testutil.FailErr(t, "delete chat", err)
	testutil.FailErr(t, "commit chat deletion", tx.Commit())
}

// The settings deadline is stamped from the timestamp the caller submitted.
func TestCreateSettingsSecretStampsTheSubmittedDeadline(t *testing.T) {
	service, _, _ := testService(t)
	meta, err := service.CreateSettingsSecret(t.Context(), CreateSettingsSecretRequest{
		ProjectID: testdbseed.DefaultProjectID, PersonID: testOwner(t, service), OperationID: "deadline-exact",
		Name: "Bounded", Purpose: "deadline is stored as submitted",
		Value: "entered-value-deadline-1", AgentUseEndsAt: "2026-09-30T12:00:00Z",
	})
	testutil.FailErr(t, "create settings secret", err)
	if meta.AgentUseEndsAt == nil || *meta.AgentUseEndsAt != "2026-09-30T12:00:00Z" {
		t.Fatalf("stored deadline = %v", meta.AgentUseEndsAt)
	}
}

// A capability whose value cannot be read is unavailable, not merely lapsed.
func TestMissingValueOutranksALapsedDeadline(t *testing.T) {
	service, values, _ := testService(t)
	meta, err := service.Generate(t.Context(), GenerateRequest{
		ProjectID: testdbseed.DefaultProjectID, ChatSessionID: "root-1", SessionID: "root-1",
		OperationID: "state-order", Name: "short-lived", Scope: ScopeChat, AgentUseTTL: time.Minute,
	})
	testutil.FailErr(t, "generate", err)
	testutil.FailErr(t, "drop stored value", values.Delete(currentValueID(t, service, meta.Reference)))

	service.now = func() time.Time { return time.Date(2026, 8, 31, 12, 5, 0, 0, time.UTC) }
	listed, err := service.ListProject(t.Context(), testdbseed.DefaultProjectID)
	testutil.FailErr(t, "list after lapse", err)
	if len(listed) != 1 || listed[0].State != StateUnavailable {
		t.Fatalf("state = %+v, want unavailable", listed)
	}
}
