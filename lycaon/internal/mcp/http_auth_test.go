package mcp_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/mcp"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestHTTPAuthHeadersTokenToken(t *testing.T) {
	h := mcp.HTTPAuthHeaders(mcp.MCPProviderEntry{
		ID:             "pagerduty",
		URL:            "https://mcp.pagerduty.com/mcp",
		Token:          "u+secret",
		CredentialWire: "token_token",
	})
	if got := h.Get("Authorization"); got != "Token token=u+secret" {
		t.Fatalf("Authorization=%q", got)
	}
}

func TestHTTPAuthHeadersNamedHeader(t *testing.T) {
	h := mcp.HTTPAuthHeaders(mcp.MCPProviderEntry{
		ID:               "custom",
		URL:              "https://example.test/mcp",
		Token:            "key-secret",
		CredentialWire:   "header",
		CredentialHeader: "X-Api-Key",
	})
	if got := h.Get("X-Api-Key"); got != "key-secret" {
		t.Fatalf("X-Api-Key=%q", got)
	}
	if got := h.Get("Authorization"); got != "" {
		t.Fatalf("Authorization=%q want empty", got)
	}
}

func TestHTTPAuthHeadersLeavesExistingAuthorization(t *testing.T) {
	h := mcp.HTTPAuthHeaders(mcp.MCPProviderEntry{
		ID:             "remote",
		URL:            "https://example.test/mcp",
		Token:          "ignored",
		CredentialWire: "token_token",
		Headers:        map[string]string{"Authorization": "already-set"},
	})
	if got := h.Get("Authorization"); got != "already-set" {
		t.Fatalf("Authorization=%q", got)
	}
}

func TestHTTPAuthHeadersAppliesBearerAndCustom(t *testing.T) {
	entry := mcp.MCPProviderEntry{
		ID:    "remote",
		URL:   "https://example.test/mcp",
		Token: "tok-secret",
		Headers: map[string]string{
			"X-Api-Key": "key-secret",
		},
	}
	h := mcp.HTTPAuthHeaders(entry)
	if got := h.Get("Authorization"); got != "Bearer tok-secret" {
		t.Fatalf("Authorization=%q", got)
	}
	if got := h.Get("X-Api-Key"); got != "key-secret" {
		t.Fatalf("X-Api-Key=%q", got)
	}
}

func TestConnectHTTPInjectsHeaders(t *testing.T) {
	var sawAuth, sawKey string
	// An empty result for every request ends the handshake at protocol
	// negotiation; only the first request's headers matter.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		sawKey = r.Header.Get("X-Api-Key")
		var req struct {
			ID json.RawMessage `json:"id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{}}`, req.ID)
	}))
	defer srv.Close()

	conn := mcp.SDKConnector{HTTPClient: srv.Client()}
	entry := mcp.MCPProviderEntry{
		ID:      "http-auth",
		URL:     srv.URL,
		Token:   "pat-never-on-get",
		Headers: map[string]string{"X-Api-Key": "hdr-never-on-get"},
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	_, _ = conn.Connect(ctx, entry, mcp.ConnectOpts{})
	if sawAuth != "Bearer pat-never-on-get" {
		t.Fatalf("Authorization=%q", sawAuth)
	}
	if sawKey != "hdr-never-on-get" {
		t.Fatalf("X-Api-Key=%q", sawKey)
	}
}

func TestWireRedactsStaticSecrets(t *testing.T) {
	dir := t.TempDir()
	storePath := filepath.Join(dir, "mcp.yaml")
	oauthPath := filepath.Join(dir, "credential-vault.age")
	stageDistro(t, `providers:
  - id: remote
    url: https://example.test/mcp
    enabled: false
`)

	secretBearer := "super-secret-bearer"
	secretHeader := "super-secret-header"
	overlay := fmt.Sprintf("providers:\n  - id: remote\n    enabled: true\n    token: %s\n    headers:\n      X-Token: %s\n", secretBearer, secretHeader)
	testutil.FailErr(t, "write user mcp", os.WriteFile(storePath, []byte(overlay), 0o600))

	reg, err := mcp.NewRuntime(mcp.RuntimeOptions{
		GlobalOverridePath: storePath,
		Connector:          &mcp.MockConnector{},
		OAuthStore:         mcp.NewOAuthTokenStoreAt(oauthPath),
	})
	testutil.FailErr(t, "NewRuntime", err)
	testutil.FailErr(t, "load", reg.Catalog.Load(t.Context()))
	row, ok := reg.Catalog.GetProvider(t.Context(), mcp.CallScope{}, "remote")
	if !ok {
		t.Fatal("missing remote")
	}
	if !row.TokenPresent {
		t.Fatal("expected token_present")
	}
	if !row.HeadersPresent {
		t.Fatal("expected headers_present")
	}
	raw, err := json.Marshal(row)
	testutil.FailErr(t, "marshal", err)
	body := string(raw)
	if strings.Contains(body, secretBearer) || strings.Contains(body, secretHeader) {
		t.Fatalf("GET body leaked secret: %s", body)
	}
	if strings.Contains(body, `"token"`) || strings.Contains(body, `"headers"`) {
		t.Fatalf("GET body must not include secret fields: %s", body)
	}
}
