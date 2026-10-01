package security

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
)

// Discovery preserves the full model namespace used to construct completion requests.
func TestListProvidersIncludesFireworks(t *testing.T) {
	t.Setenv("FIREWORKS_API_KEY", "")
	srv := wiring.BuildForTest(t).Server

	mockBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/accounts/fireworks/models") {
			_, _ = w.Write([]byte(`{"models":[{"name":"accounts/fireworks/models/llama-v3p1-8b-instruct","conversationConfig":{}}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"pong"}}]}`))
	}))
	t.Cleanup(mockBackend.Close)

	createBody := `{"id":"fireworks","base_url":` + jsonString(mockBackend.URL) + `,"kind":"fireworks"}`
	createReq := authedRequest(t, http.MethodPost, "/v1/providers", strings.NewReader(createBody))
	createReq.Header.Set("Content-Type", "application/json")
	pw := httptest.NewRecorder()
	srv.ServeHTTP(pw, createReq)
	if pw.Code != http.StatusCreated {
		t.Fatalf("create provider status = %d body = %s", pw.Code, pw.Body.String())
	}

	// Discovery only runs for a configured provider, and the fireworks kind
	// requires a key — without one the meta carries no models to check.
	credReq := authedRequest(t, http.MethodPut, "/v1/providers/fireworks/credential",
		strings.NewReader(`{"api_key":"fw-test-key"}`))
	credReq.Header.Set("Content-Type", "application/json")
	cw := httptest.NewRecorder()
	srv.ServeHTTP(cw, credReq)
	if cw.Code != http.StatusOK {
		t.Fatalf("put credential status = %d body = %s", cw.Code, cw.Body.String())
	}

	req := authedRequest(t, http.MethodGet, "/v1/providers", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	var listed wire.ProviderListResponse
	if err := json.Unmarshal(w.Body.Bytes(), &listed); err != nil {
		testutil.FailErr(t, "unmarshal JSON document", err)
	}
	providers := listed.Providers
	var fw *wire.ProviderMeta
	for i := range providers {
		if providers[i].ID == "fireworks" {
			fw = &providers[i]
			break
		}
	}
	if fw == nil {
		t.Fatal("fireworks missing from GET /v1/providers")
	}
	if len(fw.Models) < 1 {
		t.Fatal("expected at least one fireworks model in provider meta")
	}
	for _, m := range fw.Models {
		if strings.HasPrefix(m.ID, "accounts/fireworks/models/") {
			return
		}
	}
	t.Fatalf("no fireworks model ids with required prefix: %+v", fw.Models)
}

func TestFireworksProviderTestWithMockBackend(t *testing.T) {
	srv := wiring.BuildForTest(t).Server

	// Both discovery and completion requests use the fixture server.
	mockBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/accounts/fireworks/models") {
			_, _ = w.Write([]byte(`{"models":[{"name":"accounts/fireworks/models/llama-v3p1-8b-instruct","conversationConfig":{}}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"pong"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	t.Cleanup(mockBackend.Close)

	modelID := "accounts/fireworks/models/llama-v3p1-8b-instruct"
	createBody := `{"id":"fireworks","base_url":` + jsonString(mockBackend.URL) + `,"models":[{"id":` + jsonString(modelID) + `}]}`
	createReq := authedRequest(t, http.MethodPost, "/v1/providers", strings.NewReader(createBody))
	createReq.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, createReq)
	if w.Code != http.StatusCreated {
		t.Fatalf("create provider status = %d body = %s", w.Code, w.Body.String())
	}

	credReq := authedRequest(t, http.MethodPut, "/v1/providers/fireworks/credential",
		strings.NewReader(`{"api_key":"fw-test-key"}`))
	credReq.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, credReq)
	if w.Code != http.StatusOK {
		t.Fatalf("put credential status = %d body = %s", w.Code, w.Body.String())
	}

	testReq := authedRequest(t, http.MethodPost, "/v1/providers/fireworks/test",
		strings.NewReader(`{"model":`+jsonString(modelID)+`}`))
	testReq.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, testReq)
	if w.Code != http.StatusOK {
		t.Fatalf("test status = %d body = %s", w.Code, w.Body.String())
	}
	raw, err := io.ReadAll(w.Body)
	testutil.FailErr(t, "io.ReadAll failed", err)
	if strings.Contains(string(raw), "fw-test-key") {
		t.Fatalf("provider test leaked credential: %s", raw)
	}
	var result wire.ProviderProbeResult
	if err := json.Unmarshal(raw, &result); err != nil {
		testutil.FailErr(t, "unmarshal JSON document", err)
	}
	if !result.OK {
		t.Fatalf("provider test failed: %+v", result)
	}
}

func jsonString(s string) string {
	// Marshaling a string cannot fail; invalid UTF-8 becomes U+FFFD.
	b, _ := json.Marshal(s)
	return string(b)
}
