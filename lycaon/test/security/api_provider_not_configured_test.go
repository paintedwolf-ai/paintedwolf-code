package security

import (
	"encoding/json"
	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/config/configtest"
	"github.com/lycaon/lycaon/internal/configlayout"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/app"
	"github.com/lycaon/lycaon/internal/app/configuration"
	"github.com/lycaon/lycaon/internal/mcp"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

const mockLLMCatchphrase = "I understand. How can I help you further?"

// noMockServeEnv isolates credentials and disables mock LLM for production-parity tests.
func noMockServeEnv(t *testing.T) (home, projectDir string) {
	t.Helper()
	t.Setenv("LYCAON_LLM_MOCK", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("FIREWORKS_API_KEY", "")
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("LYCAON_API_TOKEN", api.TestAPIToken)
	projectDir = filepath.Join(home, "project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		testutil.FailErr(t, "create directory", err)
	}
	return home, projectDir
}

func buildNoMockServeApp(t *testing.T) *app.ServeApp {
	t.Helper()

	configtest.Overlay(t, map[config.Rel]string{
		config.DistroMCP: "providers:\n  - id: svca\n    command: \"true\"\n    args: []\n    enabled: false\n",
	})

	cfg := configuration.Config{
		ConfigRoot:                configlayout.FindModuleRoot(),
		DBPath:                    filepath.Join(t.TempDir(), "no-mock.db"),
		ListenAddr:                "127.0.0.1:0",
		TestMCPConnector:          &mcp.MockConnector{Tools: map[string][]*sdkmcp.Tool{"svca": {{Name: "do"}}}},
		TestMCPGlobalOverridePath: filepath.Join(t.TempDir(), "mcp-test.yaml"),
	}
	serveApp, err := app.Build(t.Context(), cfg)
	testutil.FailErr(t, "build app wiring harness", err)
	t.Cleanup(func() { testutil.FailErr(t, "close provider fixture", serveApp.Close()) })
	return serveApp
}

func postPromptExpectNotConfigured(t *testing.T, srv *api.Server, sessionID string) {
	t.Helper()
	postPromptAndWaitIdle(t, srv, sessionID, "hello")

	msgs := listMessagesHTTP(t, srv, sessionID)
	for _, m := range msgs {
		if m.Role == wire.MessageRoleAssistant && m.Kind != wire.MessageKindWorkflowBoundary {
			t.Fatalf("expected no LLM assistant message without provider, got %+v", m)
		}
		if strings.Contains(m.Content, mockLLMCatchphrase) {
			t.Fatalf("message leaked mock text: %+v", m)
		}
	}
}

func TestPromptWithoutProviderReturnsNotConfigured(t *testing.T) {
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	t.Cleanup(func() { project.SetDefaultOpenPolicy(project.DefaultOpenPolicy()) })

	_, projectDir := noMockServeEnv(t)
	serveApp := buildNoMockServeApp(t)
	sess := createSessionHTTP(t, serveApp.Server, projectDir)
	postPromptExpectNotConfigured(t, serveApp.Server, sess.ID)
}

func TestPromptWithoutProviderLeavesNoAssistantMessage(t *testing.T) {
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	t.Cleanup(func() { project.SetDefaultOpenPolicy(project.DefaultOpenPolicy()) })

	_, projectDir := noMockServeEnv(t)
	serveApp := buildNoMockServeApp(t)
	sess := createSessionHTTP(t, serveApp.Server, projectDir)
	postPromptExpectNotConfigured(t, serveApp.Server, sess.ID)

	req := authedRequest(t, http.MethodGet, "/v1/sessions/"+sess.ID+"/messages", nil)
	w := httptest.NewRecorder()
	serveApp.Server.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET messages status = %d", w.Code)
	}
	var page wire.SessionTranscriptPage
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		testutil.FailErr(t, "unmarshal JSON document", err)
	}
	msgs := page.Messages
	userMsgs := 0
	for _, m := range msgs {
		if wire.IsUserIntentMessage(m) {
			userMsgs++
		}
	}
	if userMsgs != 1 {
		t.Fatalf("user message count = %d want 1 (total messages = %d)", userMsgs, len(msgs))
	}
	for _, m := range msgs {
		if strings.Contains(m.Content, mockLLMCatchphrase) {
			t.Fatalf("message leaked mock text: %+v", m)
		}
	}
}

func TestPromptAfterCredentialDeleteReturnsNotConfigured(t *testing.T) {
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	t.Cleanup(func() { project.SetDefaultOpenPolicy(project.DefaultOpenPolicy()) })

	_, projectDir := noMockServeEnv(t)
	serveApp := buildNoMockServeApp(t)
	httpSrv := httptest.NewServer(serveApp.Server)
	t.Cleanup(httpSrv.Close)
	base := httpSrv.URL

	sess := createSessionHTTP(t, serveApp.Server, projectDir)

	// Keyed so the credential PUT/DELETE below is what flips configured.
	seedTestProvider(t, base, true)

	openAPIPutJSON[wire.ProviderMeta](t, base, "/v1/providers/{provider_id}/credential",
		map[string]string{"provider_id": testProviderID},
		`{"api_key":"sk-test-delete-me"}`, http.StatusOK)

	providers := openAPIGetJSON[wire.ProviderListResponse](t, base, "/v1/providers", nil, http.StatusOK).Providers
	configured := findProvider(providers, testProviderID)
	if configured == nil || !configured.Configured {
		t.Fatalf("provider should be configured after credential PUT: %+v", configured)
	}

	openAPIDo(t, base, http.MethodDelete, "/v1/providers/{provider_id}/credential",
		map[string]string{"provider_id": testProviderID}, "", http.StatusNoContent)

	providersAfter := openAPIGetJSON[wire.ProviderListResponse](t, base, "/v1/providers", nil, http.StatusOK).Providers
	after := findProvider(providersAfter, testProviderID)
	if after == nil || after.Configured {
		t.Fatalf("provider should be unconfigured after credential DELETE: %+v", after)
	}

	postPromptExpectNotConfigured(t, serveApp.Server, sess.ID)
}
