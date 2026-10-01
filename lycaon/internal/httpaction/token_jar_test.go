package httpaction

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
)

func TestTokenJarCaptureJSONAndEcho(t *testing.T) {
	secrets, _ := testSecrets(t)
	var capturedAuthHeader string
	mux := http.NewServeMux()
	mux.HandleFunc("/login", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"access_token":"super-secret-token-123","scope":"admin"}`))
	})
	mux.HandleFunc("/api/data", func(w http.ResponseWriter, r *http.Request) {
		capturedAuthHeader = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	deps := Deps{Boundary: testBoundary(), Secrets: secrets}
	root := t.TempDir()

	// Step 1: Capture token from JSON response body
	loginRes, err := runRequest(t, deps, map[string]any{
		"url":       server.URL + "/login",
		"token_jar": "test_jar",
		"capture_tokens": []any{
			map[string]any{"name": "api_tok", "from": "json:access_token"},
		},
		"capability_request": loopbackCapability(t, server.URL),
	}, sessionContext(root, "call-1"))
	testutil.FailErr(t, "login request", err)

	if loginRes.Tokens == nil || loginRes.Tokens.Captured != 1 {
		t.Fatalf("tokens receipt = %+v, want 1 captured token", loginRes.Tokens)
	}
	if issuer := loginRes.Tokens.Issuers["api_tok"]; !strings.HasPrefix(issuer, "http://127.0.0.1:") {
		t.Fatalf("issuers = %v, want the login origin recorded for api_tok", loginRes.Tokens.Issuers)
	}
	if strings.Contains(loginRes.Body, "super-secret-token-123") {
		t.Fatalf("in-memory body contains raw token: %s", loginRes.Body)
	}
	if !strings.Contains(loginRes.Body, "{{token:api_tok}}") || !loginRes.BodyRedacted {
		t.Fatalf("in-memory body does not read the token as its reference: %+v", loginRes)
	}

	// Step 2: Echo captured token in a follow-up request via {{token:api_tok}}
	apiRes, err := runRequest(t, deps, map[string]any{
		"url":       server.URL + "/api/data",
		"token_jar": "test_jar",
		"headers": []any{
			map[string]any{"name": "Authorization", "value": "Bearer {{token:api_tok}}"},
		},
		"capability_request": loopbackCapability(t, server.URL),
	}, sessionContext(root, "call-2"))
	testutil.FailErr(t, "api data request", err)

	if apiRes.Status != http.StatusOK {
		t.Fatalf("api request status = %d", apiRes.Status)
	}
	if capturedAuthHeader != "Bearer super-secret-token-123" {
		t.Fatalf("server received auth header = %q, want Bearer super-secret-token-123", capturedAuthHeader)
	}
}

func TestTokenJarCaptureHeaderAndRedaction(t *testing.T) {
	secrets, _ := testSecrets(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/auth", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Authorization", "Bearer header-secret-token-456")
		w.Header().Set("X-Token-Echo", "header-secret-token-456")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"message":"authenticated"}`))
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	deps := Deps{Boundary: testBoundary(), Secrets: secrets}
	root := t.TempDir()

	res, err := runRequest(t, deps, map[string]any{
		"url":       server.URL + "/auth",
		"token_jar": "header_jar",
		"capture_tokens": []any{
			map[string]any{"name": "hdr_tok", "from": "header:Authorization"},
		},
		"capability_request": loopbackCapability(t, server.URL),
	}, sessionContext(root, "call-1"))
	testutil.FailErr(t, "auth request", err)

	if res.Tokens == nil || res.Tokens.Captured != 1 {
		t.Fatalf("tokens receipt = %+v, want 1 captured token", res.Tokens)
	}
	for _, h := range res.Headers {
		if strings.Contains(h.Value, "header-secret-token-456") {
			t.Fatalf("header %s contains raw token: %s", h.Name, h.Value)
		}
		if h.Name == "X-Token-Echo" && !strings.Contains(h.Value, "{{token:hdr_tok}}") {
			t.Fatalf("header %s missing redaction placeholder: %s", h.Name, h.Value)
		}
	}
}

func TestTokenJarRedactsResponsePath(t *testing.T) {
	secrets, _ := testSecrets(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/download", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"token":"secret-spill-789","other":"info"}`))
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	deps := Deps{Boundary: testBoundary(), Secrets: secrets}
	root := t.TempDir()

	res, err := runRequest(t, deps, map[string]any{
		"url":           server.URL + "/download",
		"token_jar":     "spill_jar",
		"response_path": "output.json",
		"capture_tokens": []any{
			map[string]any{"name": "file_tok", "from": "json:token"},
		},
		"capability_request": loopbackCapability(t, server.URL),
	}, sessionContext(root, "call-1"))
	testutil.FailErr(t, "download request", err)

	if !res.Written || res.ResponsePath != "output.json" {
		t.Fatalf("response_path not written properly: %+v", res)
	}
	content, err := os.ReadFile(filepath.Join(root, "output.json"))
	testutil.FailErr(t, "read saved response", err)

	if strings.Contains(string(content), "secret-spill-789") {
		t.Fatalf("saved file contains raw token: %s", string(content))
	}
	if !strings.Contains(string(content), "{{token:file_tok}}") {
		t.Fatalf("saved file missing redaction placeholder: %s", string(content))
	}
}

func TestLoopbackApprovalAskRejection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	parsedURL, _ := url.Parse(server.URL)
	port, _ := strconv.Atoi(parsedURL.Port())

	registry := tools.NewDefaultRegistry()
	if err := Register(registry, Deps{Boundary: testBoundary()}); err != nil {
		t.Fatalf("register http_request: %v", err)
	}

	// 1. Without grant: should reject with isolation.CodeTryLoopbackConnect
	_, err := registry.Run(t.Context(), "http_request", map[string]any{
		"url": server.URL,
	}, tools.ToolContext{Agent: tools.DefaultToolProfileID})

	var reject *tools.ToolReject
	if !errors.As(err, &reject) || reject.Code != isolation.CodeTryLoopbackConnect {
		t.Fatalf("expected CodeTryLoopbackConnect rejection, got: %v", err)
	}
	if reject.Data["port"] != uint16(port) {
		t.Fatalf("reject port = %v, want %d", reject.Data["port"], port)
	}

	// 2. With tctx.LoopbackConnectGranted: should succeed
	out, err := registry.Run(t.Context(), "http_request", map[string]any{
		"url": server.URL,
	}, tools.ToolContext{
		Agent:                  tools.DefaultToolProfileID,
		LoopbackConnectGranted: true,
		LoopbackConnectPorts:   []uint16{uint16(port)},
	})
	testutil.FailErr(t, "granted loopback request", err)

	var res result
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if res.Status != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.Status)
	}
}
