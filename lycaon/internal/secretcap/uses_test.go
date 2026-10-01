package secretcap

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestResolutionRecordsWhoAskedAndWhatTheHostDecided(t *testing.T) {
	service, _, _ := testService(t)
	meta, err := service.CreateSettingsSecret(t.Context(), CreateSettingsSecretRequest{
		ProjectID: testdbseed.DefaultProjectID, PersonID: testOwner(t, service), OperationID: "used-1",
		Name: "Registry token", Purpose: "publishes packages", Value: "entered-value-used-0001",
	})
	testutil.FailErr(t, "create settings secret", err)

	_, err = service.Resolve(t.Context(), map[string]any{"value": meta.Reference}, ResolveContext{
		ProjectID: testdbseed.DefaultProjectID, ChatSessionID: "root-1",
		SessionID: "root-1", ToolName: "http_request",
	})
	testutil.FailErr(t, "resolve", err)

	history, err := service.Uses(t.Context(), testdbseed.DefaultProjectID, meta.Reference, 0)
	testutil.FailErr(t, "read uses", err)
	if history.Count != 1 {
		t.Fatalf("use history = %+v", history)
	}
	use := history.Items[0]
	if use.Outcome != UseResolved || use.ToolName != "http_request" || use.Version != 1 {
		t.Fatalf("recorded use = %+v", use)
	}
	if use.SessionID != "root-1" || use.ChatSessionID != "root-1" {
		t.Fatalf("recorded identity = %+v", use)
	}

	listed, err := service.ListProject(t.Context(), testdbseed.DefaultProjectID)
	testutil.FailErr(t, "list", err)
	if len(listed) != 1 || listed[0].UseCount != 1 || listed[0].LastUsedAt == nil {
		t.Fatalf("usage summary = %+v", listed)
	}
}

func TestRefusedResolutionsAreRecordedWithTheirReason(t *testing.T) {
	service, _, _ := testService(t)
	revoked, err := service.CreateSettingsSecret(t.Context(), CreateSettingsSecretRequest{
		ProjectID: testdbseed.DefaultProjectID, PersonID: testOwner(t, service), OperationID: "used-revoked",
		Name: "Revoked key", Purpose: "records its own refusals", Value: "entered-value-refuse-01",
	})
	testutil.FailErr(t, "create settings secret", err)
	_, err = service.RevokeProject(t.Context(), testdbseed.DefaultProjectID, revoked.Reference, testOwner(t, service))
	testutil.FailErr(t, "revoke", err)

	_, _ = service.Resolve(t.Context(), map[string]any{"value": revoked.Reference}, ResolveContext{
		ProjectID: testdbseed.DefaultProjectID, ChatSessionID: "root-1",
		SessionID: "root-1", ToolName: "command",
	})
	history, err := service.Uses(t.Context(), testdbseed.DefaultProjectID, revoked.Reference, 0)
	testutil.FailErr(t, "read uses", err)
	if history.Count != 1 || history.Items[0].Outcome != UseRevoked {
		t.Fatalf("refusal history = %+v", history)
	}

	scoped, err := service.Generate(t.Context(), GenerateRequest{
		ProjectID: testdbseed.DefaultProjectID, ChatSessionID: "root-1", SessionID: "root-1",
		OperationID: "used-scoped", Name: "chat key", Scope: ScopeChat,
	})
	testutil.FailErr(t, "generate chat secret", err)
	_, _ = service.Resolve(t.Context(), map[string]any{"value": scoped.Reference}, ResolveContext{
		ProjectID: testdbseed.DefaultProjectID, ChatSessionID: "root-2",
		SessionID: "root-2", ToolName: "command",
	})
	scopedHistory, err := service.Uses(t.Context(), testdbseed.DefaultProjectID, scoped.Reference, 0)
	testutil.FailErr(t, "read scoped uses", err)
	if scopedHistory.Count != 1 || scopedHistory.Items[0].Outcome != UseOutOfScope {
		t.Fatalf("out-of-scope history = %+v", scopedHistory)
	}
}

func TestUseHistoryNeverCarriesTheValueOrItsArgument(t *testing.T) {
	service, _, _ := testService(t)
	const secret = "entered-value-never-logged-1"
	meta, err := service.CreateSettingsSecret(t.Context(), CreateSettingsSecretRequest{
		ProjectID: testdbseed.DefaultProjectID, PersonID: testOwner(t, service), OperationID: "used-quiet",
		Name: "Quiet key", Purpose: "never appears in history", Value: secret,
	})
	testutil.FailErr(t, "create settings secret", err)
	_, err = service.Resolve(t.Context(), map[string]any{
		"argv": []string{"curl", "-H", "Authorization: Bearer " + meta.Reference},
	}, ResolveContext{
		ProjectID: testdbseed.DefaultProjectID, ChatSessionID: "root-1",
		SessionID: "root-1", ToolName: "command",
	})
	testutil.FailErr(t, "resolve", err)

	history, err := service.Uses(t.Context(), testdbseed.DefaultProjectID, meta.Reference, 0)
	testutil.FailErr(t, "read uses", err)
	for _, use := range history.Items {
		joined := use.ToolName + use.Outcome + use.SessionID + use.ChatSessionID + use.UsedAt
		if strings.Contains(joined, secret) || strings.Contains(joined, "Authorization") {
			t.Fatalf("use record carried call material: %+v", use)
		}
	}
}

func TestUseHistoryStaysBounded(t *testing.T) {
	service, _, _ := testService(t)
	meta, err := service.CreateSettingsSecret(t.Context(), CreateSettingsSecretRequest{
		ProjectID: testdbseed.DefaultProjectID, PersonID: testOwner(t, service), OperationID: "used-bounded",
		Name: "Busy key", Purpose: "used more than the window holds", Value: "entered-value-bounded-1",
	})
	testutil.FailErr(t, "create settings secret", err)
	for i := 0; i < maxUseHistory+5; i++ {
		_, err = service.Resolve(t.Context(), map[string]any{"value": meta.Reference}, ResolveContext{
			ProjectID: testdbseed.DefaultProjectID, ChatSessionID: "root-1",
			SessionID: "root-1", ToolName: "command",
		})
		testutil.FailErr(t, "resolve", err)
	}
	history, err := service.Uses(t.Context(), testdbseed.DefaultProjectID, meta.Reference, 0)
	testutil.FailErr(t, "read uses", err)
	if history.Count != maxUseHistory {
		t.Fatalf("retained uses = %d, want the window bound %d", history.Count, maxUseHistory)
	}
}
