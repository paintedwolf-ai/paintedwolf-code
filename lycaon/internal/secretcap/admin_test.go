package secretcap

import (
	"errors"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCreateSettingsSecretStoresProjectActWithoutSession(t *testing.T) {
	service, values, _ := testService(t)
	meta, err := service.CreateSettingsSecret(t.Context(), CreateSettingsSecretRequest{
		ProjectID: testdbseed.DefaultProjectID, PersonID: testOwner(t, service), OperationID: "create-settings-1",
		Name: "Stripe test key", Purpose: "checkout integration against test mode",
		Value: "sk-test-entered-value-0001",
	})
	testutil.FailErr(t, "create settings secret", err)
	if meta.Origin != OriginSettingsEntered || meta.Scope != ScopeProject || meta.ChatSessionID != nil {
		t.Fatalf("entered metadata = %+v", meta)
	}
	if meta.Version != 1 || meta.ValueReplacedAt != nil || meta.UseCount != 0 || meta.State != StateActive {
		t.Fatalf("entered lifecycle facts = %+v", meta)
	}
	if value, ok := values.Get(currentValueID(t, service, meta.Reference)); !ok || value != "sk-test-entered-value-0001" {
		t.Fatalf("stored value = %q ok=%v", value, ok)
	}
}

func TestCreateSettingsSecretRefusesMissingPurpose(t *testing.T) {
	service, _, _ := testService(t)
	_, err := service.CreateSettingsSecret(t.Context(), CreateSettingsSecretRequest{
		ProjectID: testdbseed.DefaultProjectID, PersonID: testOwner(t, service), OperationID: "create-settings-no-purpose",
		Name: "Unlabelled key", Value: "entered-value-without-purpose",
	})
	if !errors.Is(err, ErrInvalidPut) {
		t.Fatalf("missing-purpose error = %v", err)
	}
}

func TestUpdateRelabelsWithoutTouchingProvenanceOrValue(t *testing.T) {
	service, values, _ := testService(t)
	meta, err := service.CreateSettingsSecret(t.Context(), CreateSettingsSecretRequest{
		ProjectID: testdbseed.DefaultProjectID, PersonID: testOwner(t, service), OperationID: "update-labels",
		Name: "Old name", Purpose: "old purpose", Value: "entered-value-relabel-1",
	})
	testutil.FailErr(t, "create settings secret", err)
	valueID := currentValueID(t, service, meta.Reference)

	name, purpose := "New name", "new purpose"
	updated, err := service.Update(t.Context(), UpdateRequest{
		ProjectID: testdbseed.DefaultProjectID, Reference: meta.Reference,
		Name: &name, Purpose: &purpose,
	})
	testutil.FailErr(t, "update labels", err)
	if updated.Name != name || updated.Purpose != purpose {
		t.Fatalf("relabelled metadata = %+v", updated)
	}
	if updated.Origin != OriginSettingsEntered || updated.CreatedAt != meta.CreatedAt || updated.Version != 1 {
		t.Fatalf("relabel changed provenance: %+v", updated)
	}
	if got := currentValueID(t, service, meta.Reference); got != valueID {
		t.Fatal("relabel replaced the stored value")
	}
	if value, ok := values.Get(valueID); !ok || value != "entered-value-relabel-1" {
		t.Fatalf("stored value after relabel = %q ok=%v", value, ok)
	}
}

func TestAgentUseDeadlineIsSetClearedAndRevivable(t *testing.T) {
	service, _, _ := testService(t)
	meta, err := service.Generate(t.Context(), GenerateRequest{
		ProjectID: testdbseed.DefaultProjectID, ChatSessionID: "root-1", SessionID: "root-1",
		OperationID: "agent-use-deadline-admin", Name: "short-lived", Scope: ScopeChat, AgentUseTTL: time.Minute,
	})
	testutil.FailErr(t, "generate", err)

	service.now = func() time.Time { return time.Date(2026, 8, 31, 12, 5, 0, 0, time.UTC) }
	listed, err := service.ListProject(t.Context(), testdbseed.DefaultProjectID)
	testutil.FailErr(t, "list after lapse", err)
	if len(listed) != 1 || listed[0].State != StateAgentUseExpired {
		t.Fatalf("lapsed state = %+v", listed)
	}

	revived, err := service.Update(t.Context(), UpdateRequest{
		ProjectID: testdbseed.DefaultProjectID, Reference: meta.Reference,
		AgentUseDeadline: &AgentUseDeadline{At: "2026-09-30T12:00:00Z"},
	})
	testutil.FailErr(t, "extend agent-use deadline", err)
	if revived.State != StateActive || revived.AgentUseEndsAt == nil {
		t.Fatalf("revived metadata = %+v", revived)
	}

	cleared, err := service.Update(t.Context(), UpdateRequest{
		ProjectID: testdbseed.DefaultProjectID, Reference: meta.Reference, AgentUseDeadline: &AgentUseDeadline{},
	})
	testutil.FailErr(t, "clear agent-use deadline", err)
	if cleared.AgentUseEndsAt != nil || cleared.State != StateActive {
		t.Fatalf("cleared metadata = %+v", cleared)
	}
}

func TestAgentUseDeadlineRefusesPastOrBeyondAYear(t *testing.T) {
	service, _, _ := testService(t)
	meta, err := service.CreateSettingsSecret(t.Context(), CreateSettingsSecretRequest{
		ProjectID: testdbseed.DefaultProjectID, PersonID: testOwner(t, service), OperationID: "agent-use-deadline-bounds",
		Name: "Bounded", Purpose: "deadline bounds", Value: "entered-value-bounds-01",
	})
	testutil.FailErr(t, "create settings secret", err)
	for _, at := range []string{"2026-08-30T12:00:00Z", "2030-01-01T00:00:00Z", "not-a-timestamp"} {
		_, err := service.Update(t.Context(), UpdateRequest{
			ProjectID: testdbseed.DefaultProjectID, Reference: meta.Reference, AgentUseDeadline: &AgentUseDeadline{At: at},
		})
		if !errors.Is(err, ErrInvalidUpdate) {
			t.Fatalf("agent-use deadline %q error = %v", at, err)
		}
	}
}

func TestScopePromotionReachesOtherChatsAndRefusesNarrowing(t *testing.T) {
	service, _, _ := testService(t)
	meta, err := service.Generate(t.Context(), GenerateRequest{
		ProjectID: testdbseed.DefaultProjectID, ChatSessionID: "root-1", SessionID: "root-1",
		OperationID: "promote-me", Name: "deploy token", Scope: ScopeChat,
	})
	testutil.FailErr(t, "generate chat secret", err)

	scope := ScopeProject
	if _, err := service.Update(t.Context(), UpdateRequest{
		ProjectID: testdbseed.DefaultProjectID, Reference: meta.Reference, Scope: &scope,
	}); !errors.Is(err, ErrInvalidUpdate) {
		t.Fatalf("promotion without a purpose error = %v", err)
	}

	purpose := "publishes releases for any later chat"
	promoted, err := service.Update(t.Context(), UpdateRequest{
		ProjectID: testdbseed.DefaultProjectID, Reference: meta.Reference,
		Purpose: &purpose, Scope: &scope,
	})
	testutil.FailErr(t, "promote", err)
	if promoted.Scope != ScopeProject || promoted.ChatSessionID != nil {
		t.Fatalf("promoted metadata = %+v", promoted)
	}

	listed, err := service.List(t.Context(), testdbseed.DefaultProjectID, "root-2")
	testutil.FailErr(t, "list from another chat", err)
	if len(listed) != 1 || listed[0].Reference != meta.Reference || listed[0].State != StateActive {
		t.Fatalf("promoted capability is not visible from another chat: %+v", listed)
	}

	chat := ScopeChat
	if _, err := service.Update(t.Context(), UpdateRequest{
		ProjectID: testdbseed.DefaultProjectID, Reference: meta.Reference, Scope: &chat,
	}); !errors.Is(err, ErrInvalidUpdate) {
		t.Fatalf("narrowing error = %v", err)
	}
}

func TestRevocationIsTerminalForEveryAdministrationPath(t *testing.T) {
	service, _, _ := testService(t)
	meta, err := service.CreateSettingsSecret(t.Context(), CreateSettingsSecretRequest{
		ProjectID: testdbseed.DefaultProjectID, PersonID: testOwner(t, service), OperationID: "terminal",
		Name: "Doomed", Purpose: "about to be revoked", Value: "entered-value-terminal-1",
	})
	testutil.FailErr(t, "create settings secret", err)
	_, err = service.RevokeProject(t.Context(), testdbseed.DefaultProjectID, meta.Reference, testOwner(t, service))
	testutil.FailErr(t, "revoke", err)

	name := "renamed after revocation"
	if _, err := service.Update(t.Context(), UpdateRequest{
		ProjectID: testdbseed.DefaultProjectID, Reference: meta.Reference, Name: &name,
	}); !errors.Is(err, ErrRevoked) {
		t.Fatalf("update after revocation error = %v", err)
	}
	if _, err := service.ReplaceValue(t.Context(), ReplaceValueRequest{
		ProjectID: testdbseed.DefaultProjectID, Reference: meta.Reference, Value: "entered-value-terminal-2",
	}); !errors.Is(err, ErrRevoked) {
		t.Fatalf("value replacement after revocation error = %v", err)
	}
}

func TestAdministrationRefusesAnotherProjectsCapability(t *testing.T) {
	service, _, _ := testService(t)
	meta, err := service.CreateSettingsSecret(t.Context(), CreateSettingsSecretRequest{
		ProjectID: testdbseed.DefaultProjectID, PersonID: testOwner(t, service), OperationID: "foreign",
		Name: "Scoped", Purpose: "scoped to the seeded project", Value: "entered-value-foreign-1",
	})
	testutil.FailErr(t, "create settings secret", err)
	name := "renamed from elsewhere"
	if _, err := service.Update(t.Context(), UpdateRequest{
		ProjectID: "00000000-0000-4000-8000-0000000000ff", Reference: meta.Reference, Name: &name,
	}); !errors.Is(err, ErrNotVisible) {
		t.Fatalf("cross-project update error = %v", err)
	}
}
