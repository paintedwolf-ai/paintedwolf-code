package mcpadmin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/credentialstore"
	"github.com/lycaon/lycaon/internal/mcp"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/apitestdeps"
	"github.com/lycaon/lycaon/internal/usernotice"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestWriteMCPAdminErrorRendersCatalogCopy(t *testing.T) {
	s := testHandler(t)
	rec := httptest.NewRecorder()
	s.writeMCPAdminError(rec, mcp.AdminErrID(mcp.RejectRemoteRequiresHTTPS, "coropa"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", rec.Code)
	}
	var resp wire.ErrorResponse
	testutil.FailErr(t, "decode", json.Unmarshal(rec.Body.Bytes(), &resp))
	if resp.Code != "remote_requires_https" {
		t.Fatalf("code = %q", resp.Code)
	}
	if !strings.Contains(resp.Title, "HTTPS") {
		t.Fatalf("title = %q", resp.Title)
	}
	if resp.Message == "" || resp.SuggestedAction == "" {
		t.Fatalf("incomplete notice: %+v", resp)
	}
}

func TestWriteMCPAdminErrorClassifiesUncodedSync(t *testing.T) {
	s := testHandler(t)
	rec := httptest.NewRecorder()
	s.writeMCPAdminError(rec, errors.New("register_tool: duplicate"))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d", rec.Code)
	}
	var resp wire.ErrorResponse
	testutil.FailErr(t, "decode", json.Unmarshal(rec.Body.Bytes(), &resp))
	if resp.Code != "mcp_sync_failed" {
		t.Fatalf("code = %q body=%s", resp.Code, rec.Body.String())
	}
}

func TestDecorateMCPProviderAttachesNotice(t *testing.T) {
	s := testHandler(t)
	row := s.decorateMCPProvider(wire.McpProvider{
		ID:        "evil",
		LastError: mcp.RejectProjectStdioForbidden,
	})
	if row.Notice == nil {
		t.Fatal("expected notice on a coded last_error")
	}
	if row.Notice.Title == "" || row.Notice.Message == "" {
		t.Fatalf("incomplete notice: %+v", row.Notice)
	}
}

func TestMCPFailureNoticesDoNotInventProviderState(t *testing.T) {
	s := testHandler(t)
	cases := []struct {
		name string
		err  error
		code string
	}{
		{"missing executable", &os.PathError{Op: "fork/exec", Path: "/missing-provider", Err: os.ErrNotExist}, mcp.CodeSyncFailed},
		{"handshake EOF", fmt.Errorf("initialize: %w", io.EOF), mcp.CodeSyncFailed},
		{"invalid tool metadata", errors.New("invalid tool schema"), mcp.CodeSyncFailed},
		{"refused connection", fmt.Errorf("dial: %w", syscall.ECONNREFUSED), mcp.CodeProviderUnreachable},
		{"connection lost after response", fmt.Errorf("read: %w", syscall.ECONNRESET), mcp.CodeProviderUnreachable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code := mcp.SyncFailureCode(tc.err)
			if code != tc.code {
				t.Fatalf("code = %q, want %q", code, tc.code)
			}
			rec := httptest.NewRecorder()
			s.writeMCPAdminError(rec, tc.err)
			var response wire.ErrorResponse
			testutil.FailErr(t, "decode notice", json.Unmarshal(rec.Body.Bytes(), &response))
			provider := s.decorateMCPProvider(wire.McpProvider{ID: "fixture", Enabled: true, LastError: code})
			check := s.decorateMCPCheckRow(wire.McpCheckRow{ProviderID: "fixture", Code: code})
			if provider.Notice == nil || check.Notice == nil {
				t.Fatal("missing provider or check notice")
			}
			for _, message := range []string{response.Message, provider.Notice.Message, check.Notice.Message} {
				if message == "" {
					t.Fatal("empty failure message")
				}
				for _, claim := range []string{"provider answered", "settings were not changed"} {
					if strings.Contains(message, claim) {
						t.Errorf("failure invents state %q: %s", claim, message)
					}
				}
			}
			if !provider.Enabled {
				t.Fatal("decoration changed the saved enable flag")
			}
		})
	}
}

func testHandler(t *testing.T) *Handler {
	t.Helper()
	cfg, err := usernotice.LoadNoticeDir(filepath.Join("..", "..", "..", "config", "packs", "painted-wolf", "platform", "host", "user-notices"))
	testutil.FailErr(t, "load notice catalog", err)
	deps := apitestdeps.Deps{}
	apitestdeps.Fill(t, &deps)
	return New(deps.MCP, deps.Projects, &httpio.Responder{Notices: usernotice.NewCatalog(cfg)})
}

func TestHandlerPostCommitFinalizationDeferredReportsSuccessAtAPIBoundary(t *testing.T) {
	cfg, err := usernotice.LoadNoticeDir(filepath.Join("..", "..", "..", "config", "packs", "painted-wolf", "platform", "host", "user-notices"))
	testutil.FailErr(t, "load notice catalog", err)
	stateDir := t.TempDir()
	failedFinalizationOnce := false
	reg, err := mcp.NewRuntime(mcp.RuntimeOptions{
		StatePath:          stateDir,
		GlobalOverridePath: filepath.Join(stateDir, "mcp.yaml"),
		OAuthStore:         mcp.NewOAuthTokenStoreAt(filepath.Join(stateDir, "mcp-oauth.vault")),
		Connector:          &mcp.MockConnector{},
		VerifyStaticCredentialWrite: func(slot credentialstore.Slot, key, want string) error {
			if !failedFinalizationOnce && strings.Contains(want, `"tok-v2"`) && !strings.Contains(want, `"pending_commit"`) {
				failedFinalizationOnce = true
				return errors.New("injected post-commit finalization verification failure")
			}
			return credentialstore.VerifyDurable(slot, key, want)
		},
	})
	testutil.FailErr(t, "mcp registry", err)
	t.Cleanup(func() { _ = reg.Close(t.Context()) })
	deps := apitestdeps.Deps{MCP: reg}
	apitestdeps.Fill(t, &deps)
	s := New(deps.MCP, deps.Projects, &httpio.Responder{Notices: usernotice.NewCatalog(cfg)})

	createBody := `{"source":"custom","id":"api-deferred","url":"https://mcp.example.com/v1","enabled":false,"token":"tok-v1"}`
	createReq := httptest.NewRequest(http.MethodPost, "/v1/mcp/providers", strings.NewReader(createBody))
	createReq.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	s.CreateProvider(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("CreateProvider status = %d, want 201; body=%s", createRec.Code, createRec.Body.String())
	}
	var created wire.McpProvider
	testutil.FailErr(t, "decode created provider", json.Unmarshal(createRec.Body.Bytes(), &created))
	if created.ID != "api-deferred" || created.URL != "https://mcp.example.com/v1" || !created.TokenPresent {
		t.Fatalf("created provider = %+v", created)
	}

	updateBody := `{"url":"https://mcp.example.com/v2","token":"tok-v2"}`
	updateReq := httptest.NewRequest(http.MethodPatch, "/v1/mcp/providers/api-deferred", strings.NewReader(updateBody))
	updateReq.Header.Set("Content-Type", "application/json")
	route := chi.NewRouteContext()
	route.URLParams.Add("provider_id", "api-deferred")
	updateReq = updateReq.WithContext(context.WithValue(updateReq.Context(), chi.RouteCtxKey, route))
	updateRec := httptest.NewRecorder()
	s.UpdateProvider(updateRec, updateReq)
	if updateRec.Code != http.StatusOK {
		t.Fatalf("UpdateProvider status = %d, want 200; body=%s", updateRec.Code, updateRec.Body.String())
	}
	var updated wire.McpProvider
	testutil.FailErr(t, "decode updated provider", json.Unmarshal(updateRec.Body.Bytes(), &updated))
	if updated.URL != "https://mcp.example.com/v2" || !updated.TokenPresent {
		t.Fatalf("updated provider = %+v", updated)
	}

	testutil.FailErr(t, "Catalog.Load finalizes deferred commit", s.catalog.Load(context.Background()))
	row, ok := s.catalog.GetProvider(context.Background(), mcp.CallScope{}, "api-deferred")
	if !ok || row.URL != "https://mcp.example.com/v2" || !row.TokenPresent {
		t.Fatalf("reloaded provider = %+v (ok=%v)", row, ok)
	}
}
