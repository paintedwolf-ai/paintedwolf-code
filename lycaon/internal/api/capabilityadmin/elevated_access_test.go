package capabilityadmin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func elevatedTestHandler(t *testing.T) (*Handler, *wire.Session) {
	t.Helper()
	approvals, err := settings.NewApprovalStoreAt(filepath.Join(t.TempDir(), "approvals.yaml"))
	testutil.FailErr(t, "create approvals", err)
	sessions := store.NewMemory()
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	projects := project.NewMemoryRegistry()
	p, err := project.CreateWithRoot(t.Context(), projects, t.TempDir())
	testutil.FailErr(t, "create project", err)
	chat, err := sessions.Create(t.Context(), wire.CreateSessionRequest{}, p.ID)
	testutil.FailErr(t, "create chat", err)
	handler := New(&httpio.Responder{Logger: slog.Default()}, withHost(t, Deps{Projects: projects, Settings: &settings.Service{Approvals: approvals}, Gate: settings.NewRuleApprovalGate(approvals, settings.NoSources()), Store: sessions}))
	return &handler, chat
}

func addElevatedGrant(t *testing.T, s *Handler, chat *wire.Session, id string) {
	t.Helper()
	_, err := s.Gate.ApplyGrant(hitl.ApprovalGrant{ID: id, Scope: hitl.ApprovalGrantScopeChat,
		ChatSessionID: chat.ID, ProjectID: chat.ProjectID,
		Predicate: hitl.ApprovalGrantPredicate{Category: hitl.ApprovalGrantCategoryExecutionCapability, Pattern: "host_execution"}})
	testutil.FailErr(t, "install host execution", err)
}

func TestElevatedAccessDisabledPolicyPreservesSavedAuthority(t *testing.T) {
	s, chat := elevatedTestHandler(t)
	addElevatedGrant(t, s, chat, "grant_host")
	if got := elevatedSummary(t, s, chat); !got.ApprovalsEnabled || got.Total != 1 {
		t.Fatalf("enabled summary = %+v", got)
	}
	off := true
	testutil.FailErr(t, "disable approvals", s.Settings.Approvals.PutGlobal(settings.ApprovalConfig{NeverAsk: &off}))
	summary := elevatedSummary(t, s, chat)
	if summary.ApprovalsEnabled || summary.Total != 0 || len(summary.Records) != 0 {
		t.Fatalf("disabled summary = %+v", summary)
	}
	result := revokeElevated(t, s, chat.ID)
	if len(result.Results) != 0 || len(s.Gate.ListGrants(chat.ID)) != 1 {
		t.Fatal("disabled revoke changed saved authority")
	}
	off = false
	testutil.FailErr(t, "enable approvals", s.Settings.Approvals.PutGlobal(settings.ApprovalConfig{NeverAsk: &off}))
	if got := elevatedSummary(t, s, chat); !got.ApprovalsEnabled || got.Total != 1 {
		t.Fatalf("restored summary = %+v", got)
	}
}

func TestElevatedAccessSelectsOnlyApplicableLiveAuthority(t *testing.T) {
	s, chat := elevatedTestHandler(t)
	addElevatedGrant(t, s, chat, "grant_live")
	other := *chat
	other.ID = "other-chat"
	addElevatedGrant(t, s, &other, "grant_other")
	other.ID = chat.ID
	other.ProjectID = "other-project"
	addElevatedGrant(t, s, &other, "grant_other_project")
	expired := time.Now().Add(-time.Hour)
	for _, grant := range []hitl.ApprovalGrant{
		{ID: "grant_expired", Scope: hitl.ApprovalGrantScopeChat, ChatSessionID: chat.ID, ProjectID: chat.ProjectID, ExpiresAt: &expired, Predicate: hitl.ApprovalGrantPredicate{Category: "action_set"}, ElevatedEffects: []wire.ElevatedAccessEffect{wire.ElevatedAccessEffectHostExecution}},
		{ID: "grant_ordinary", Scope: hitl.ApprovalGrantScopeChat, ChatSessionID: chat.ID, ProjectID: chat.ProjectID, Predicate: hitl.ApprovalGrantPredicate{Category: "host", Pattern: "example.com"}},
	} {
		_, err := s.Gate.ApplyGrant(grant)
		testutil.FailErr(t, "install fixture", err)
	}
	s.Gate.PutAskQuiet(hitl.AskQuiet{ID: "quiet_elevated", ChatSessionID: chat.ID, Key: "elevated", ElevatedEffects: []wire.ElevatedAccessEffect{wire.ElevatedAccessEffectDirectNetwork}}, 0)
	s.Gate.PutAskQuiet(hitl.AskQuiet{ID: "quiet_ordinary", ChatSessionID: chat.ID, Key: "ordinary"}, 0)
	if got := elevatedSummary(t, s, chat); got.Total != 2 {
		t.Fatalf("selected = %+v", got)
	}
	result := revokeElevated(t, s, chat.ID)
	if len(result.Results) != 2 || result.Remaining.Total != 0 {
		t.Fatalf("revoke = %+v", result)
	}
	if len(s.Gate.ListGrants("other-chat")) == 0 {
		t.Fatal("removed another chat's authority")
	}
	if len(s.Gate.ListAskQuiets(chat.ID)) != 1 {
		t.Fatal("removed ordinary quiet")
	}
}

func TestElevatedAccessRevokeHasNoBulkIDCeiling(t *testing.T) {
	s, chat := elevatedTestHandler(t)
	for i := range 300 {
		addElevatedGrant(t, s, chat, fmt.Sprintf("grant_%d", i))
	}
	result := revokeElevated(t, s, chat.ID)
	if len(result.Results) != 300 || result.Remaining.Total != 0 {
		t.Fatalf("revoked %d; remaining %d", len(result.Results), result.Remaining.Total)
	}
	if got := revokeElevated(t, s, chat.ID); len(got.Results) != 0 {
		t.Fatal("repeat revoke was not idempotent")
	}
}

type failedElevatedLedger struct{}

func (failedElevatedLedger) ForgetChatGrant(context.Context, string) (bool, error) {
	return false, errors.New("storage unavailable: private diagnostic")
}

func TestElevatedAccessDurableFailureRemainsRetryable(t *testing.T) {
	s, chat := elevatedTestHandler(t)
	addElevatedGrant(t, s, chat, "grant_failed")
	s.ChatGrants = failedElevatedLedger{}
	result := revokeElevated(t, s, chat.ID)
	if len(result.Results) != 1 || result.Results[0].Disposition != "failed" || result.Remaining.Total != 1 {
		t.Fatalf("result = %+v", result)
	}
	if result.Results[0].Code != wire.ApiErrorCodeInternalError || result.Results[0].Message != "the approval could not be revoked" {
		t.Fatalf("durable failure diagnostic = %+v", result.Results[0])
	}
	s.ChatGrants = &fakeChatGrantLedger{ids: map[string]bool{"grant_failed": true}}
	result = revokeElevated(t, s, chat.ID)
	if result.Remaining.Total != 0 || result.Results[0].Disposition != "revoked" {
		t.Fatalf("retry = %+v", result)
	}
}

func revokeElevated(t *testing.T, s *Handler, id string) wire.RevokeElevatedAccessResponse {
	t.Helper()
	router := chi.NewRouter()
	router.Post("/v1/sessions/{id}/elevated-access/revoke", s.HandleRevokeElevatedAccess)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/sessions/"+id+"/elevated-access/revoke", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("revoke: %d %s", response.Code, response.Body.String())
	}
	var result wire.RevokeElevatedAccessResponse
	testutil.FailErr(t, "decode revoke", json.Unmarshal(response.Body.Bytes(), &result))
	return result
}

func elevatedSummary(t *testing.T, s *Handler, chat *wire.Session) wire.ElevatedAccessSummary {
	t.Helper()
	summary, err := s.elevatedAccessSummary(t.Context(), chat)
	testutil.FailErr(t, "read elevated summary", err)
	return summary
}

func TestElevatedAccessProjectRestoresApprovals(t *testing.T) {
	s, chat := elevatedTestHandler(t)
	addElevatedGrant(t, s, chat, "grant_host")
	off := true
	testutil.FailErr(t, "disable device approvals", s.Settings.Approvals.PutGlobal(settings.ApprovalConfig{NeverAsk: &off}))
	p, err := s.Projects.Get(t.Context(), chat.ProjectID)
	testutil.FailErr(t, "read project", err)
	overlay, err := project.ResolveProjectOverlay(p, "")
	testutil.FailErr(t, "resolve overlay", err)
	off = false
	testutil.FailErr(t, "restore project approvals", s.Settings.Approvals.PutProject(overlay.Primary.Path, settings.ApprovalConfig{NeverAsk: &off}))
	if got := elevatedSummary(t, s, chat); !got.ApprovalsEnabled || got.Total != 1 {
		t.Fatalf("project restoration = %+v", got)
	}
}

func TestElevatedAccessSharedScopesAndNoProject(t *testing.T) {
	s, chat := elevatedTestHandler(t)
	for _, item := range []struct {
		id      string
		scope   hitl.ApprovalGrantScope
		project string
	}{
		{"grant_project", hitl.ApprovalGrantScopeProject, chat.ProjectID},
		{"grant_device", hitl.ApprovalGrantScopeDevice, ""},
		{"grant_other", hitl.ApprovalGrantScopeProject, "other-project"},
	} {
		_, err := s.Gate.ApplyGrant(hitl.ApprovalGrant{ID: item.id, Scope: item.scope, ProjectID: item.project,
			GrantedByPersonID: "person-test", Title: "Local service", Predicate: hitl.ApprovalGrantPredicate{Category: "host_resource", Pattern: "service"},
			ElevatedEffects: []wire.ElevatedAccessEffect{wire.ElevatedAccessEffectLocalService}})
		testutil.FailErr(t, "install shared grant", err)
	}
	got := elevatedSummary(t, s, chat)
	if got.Total != 2 || len(got.SharedScopes) != 2 {
		t.Fatalf("shared selection = %+v", got)
	}
	standalone := &wire.Session{ID: "no-project"}
	if got := elevatedSummary(t, s, standalone); got.Total != 1 || got.Records[0].ID != "grant_device" {
		t.Fatalf("no-project selection = %+v", got)
	}
	result := revokeElevated(t, s, chat.ID)
	if len(result.Results) != 2 || result.Remaining.Total != 0 {
		t.Fatalf("shared revoke = %+v", result)
	}
	if len(s.Settings.Approvals.GlobalGrants()) != 1 {
		t.Fatal("shared revoke removed unrelated project authority")
	}
}

func TestElevatedAccessWorkerResolvesRoot(t *testing.T) {
	s, chat := elevatedTestHandler(t)
	addElevatedGrant(t, s, chat, "grant_root")
	worker, err := s.Store.CreateChild(t.Context(), chat, wire.SpawnChildRequest{AgentType: "coder"})
	testutil.FailErr(t, "create worker", err)
	router := chi.NewRouter()
	router.Get("/v1/sessions/{id}/elevated-access", s.HandleGetElevatedAccess)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/sessions/"+worker.ID+"/elevated-access", nil))
	var summary wire.ElevatedAccessSummary
	testutil.FailErr(t, "decode worker summary", json.Unmarshal(response.Body.Bytes(), &summary))
	if response.Code != http.StatusOK || summary.RootSessionID != chat.ID || summary.Total != 1 {
		t.Fatalf("worker summary = %d %+v", response.Code, summary)
	}
	if result := revokeElevated(t, s, worker.ID); len(result.Results) != 1 || result.Remaining.Total != 0 {
		t.Fatalf("worker revoke = %+v", result)
	}
}

func TestElevatedAccessUsesChatsActiveOverlay(t *testing.T) {
	s, primaryChat := elevatedTestHandler(t)
	off := true
	testutil.FailErr(t, "disable device approvals", s.Settings.Approvals.PutGlobal(settings.ApprovalConfig{NeverAsk: &off}))
	root, err := s.Projects.AttachRoot(t.Context(), primaryChat.ProjectID, project.AttachRootParams{Path: t.TempDir()})
	testutil.FailErr(t, "attach secondary root", err)
	on := false
	testutil.FailErr(t, "restore secondary-root approvals", s.Settings.Approvals.PutProject(root.Added.Path, settings.ApprovalConfig{NeverAsk: &on}))
	secondaryChat, err := s.Store.Create(t.Context(), wire.CreateSessionRequest{WorkspaceRootID: root.Added.ID}, primaryChat.ProjectID)
	testutil.FailErr(t, "create secondary-root chat", err)
	addElevatedGrant(t, s, secondaryChat, "grant_secondary")
	if got := elevatedSummary(t, s, secondaryChat); !got.ApprovalsEnabled || got.Total != 1 {
		t.Fatalf("secondary overlay = %+v", got)
	}
	if got := elevatedSummary(t, s, primaryChat); got.ApprovalsEnabled {
		t.Fatal("secondary overlay restored approvals for the primary-root chat")
	}
}
