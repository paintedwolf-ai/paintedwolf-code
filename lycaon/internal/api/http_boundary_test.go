package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lycaon/lycaon/internal/api/httpio"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestRouterErrorsUseCanonicalJSONContract(t *testing.T) {
	srv := newTestServer(t)
	tests := []struct {
		name       string
		method     string
		path       string
		wantStatus int
		wantCode   wire.ApiErrorCode
	}{
		{name: "not found", method: http.MethodGet, path: "/missing", wantStatus: http.StatusNotFound, wantCode: wire.ApiErrorCodeNotFound},
		{name: "method not allowed", method: http.MethodPost, path: "/health", wantStatus: http.StatusMethodNotAllowed, wantCode: wire.ApiErrorCodeMethodNotAllowed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			srv.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
			if w.Code != tc.wantStatus || w.Header().Get("Content-Type") != httpio.MediaTypeJSON {
				t.Fatalf("status=%d content-type=%q body=%s", w.Code, w.Header().Get("Content-Type"), w.Body.String())
			}
			var body wire.ErrorResponse
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode error response: %v", err)
			}
			if body.Code != tc.wantCode {
				t.Fatalf("code=%q want=%q", body.Code, tc.wantCode)
			}
		})
	}
}

func TestMalformedV1PathStillPassesCORSAndAuthentication(t *testing.T) {
	srv := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/v1//projects", nil)
	req.Header.Set("Origin", "tauri://localhost")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	assertErrorResponse(t, w, http.StatusUnauthorized, "unauthorized")
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "tauri://localhost" {
		t.Fatalf("Access-Control-Allow-Origin = %q", got)
	}
}

func TestMethodNotAllowedHasCanonicalAllowHeader(t *testing.T) {
	srv := newTestServer(t)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/health", nil))
	if got := w.Header().Get("Allow"); got != http.MethodGet {
		t.Fatalf("Allow = %q, want GET", got)
	}
}
