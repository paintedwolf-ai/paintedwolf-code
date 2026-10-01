package mcpadmin

import (
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

	"github.com/lycaon/lycaon/internal/api/httpio"
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
	if strings.Contains(resp.Message, "did not complete") {
		t.Fatal("generic fallback leaked onto a classified MCP failure")
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
				for _, claim := range []string{"provider answered", "did not answer", "settings were not changed"} {
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
