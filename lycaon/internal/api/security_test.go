package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestCORSDefaultLocalhost(t *testing.T) {
	srv := newTestServer(t)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodOptions, "/health", nil)
	req.Header.Set("Origin", "http://localhost:1420")
	req.Header.Set("Access-Control-Request-Method", "GET")
	req.Header.Set("Access-Control-Request-Headers", "authorization,if-none-match")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:1420" {
		t.Fatalf("Allow-Origin = %q", got)
	}
	if got := w.Header().Get("Access-Control-Allow-Headers"); !strings.Contains(strings.ToLower(got), "if-none-match") {
		t.Fatalf("Allow-Headers = %q, want If-None-Match", got)
	}
}

func TestCORSRejectsUnknownOrigin(t *testing.T) {
	srv := newTestServer(t)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodOptions, "/health", nil)
	req.Header.Set("Origin", "http://evil.example")
	req.Header.Set("Access-Control-Request-Method", "GET")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if got := w.Header().Get("Access-Control-Allow-Origin"); got == "http://evil.example" {
		t.Fatalf("unexpected Allow-Origin = %q", got)
	}
}

func TestCORSExposesClientResponseMetadata(t *testing.T) {
	srv := newTestServer(t)
	for _, origin := range productionCORSOrigins {
		req := newAuthedRequest(http.MethodGet, "/v1/projects", nil)
		req.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		if w.Header().Get("Access-Control-Allow-Origin") != origin {
			t.Fatalf("origin %q was refused", origin)
		}
		exposed := make(map[string]bool)
		for _, name := range strings.Split(w.Header().Get("Access-Control-Expose-Headers"), ",") {
			exposed[http.CanonicalHeaderKey(strings.TrimSpace(name))] = true
		}
		for _, name := range []string{"Content-Disposition", "X-Export-Truncated", "Etag", "Retry-After"} {
			if !exposed[name] {
				t.Errorf("origin %q cannot read %s", origin, name)
			}
		}
	}
}

func TestCORSDevModeIgnoredWithoutDevFlag(t *testing.T) {
	t.Setenv("LYCAON_DEV_CORS", "1")

	srv := NewServer(requiredTestDeps(t, Dependencies{Store: store.NewMemory(), Projects: project.NewMemoryRegistry()}), slog.Default(), TestAPIToken)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodOptions, "/health", nil)
	req.Header.Set("Origin", "http://evil.example")
	req.Header.Set("Access-Control-Request-Method", "GET")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if got := w.Header().Get("Access-Control-Allow-Origin"); got == "*" {
		t.Fatalf("Allow-Origin = %q, want strict localhost CORS", got)
	}
}

func TestCORSDevModeAllowsAnyOrigin(t *testing.T) {
	t.Setenv("LYCAON_DEV_CORS", "1")
	t.Setenv("LYCAON_DEV", "1")

	srv := NewServer(requiredTestDeps(t, Dependencies{Store: store.NewMemory(), Projects: project.NewMemoryRegistry()}), slog.Default(), TestAPIToken)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodOptions, "/health", nil)
	req.Header.Set("Origin", "http://evil.example")
	req.Header.Set("Access-Control-Request-Method", "GET")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("Allow-Origin = %q, want *", got)
	}
}

func TestCreateSessionInvalidMode(t *testing.T) {
	dir := t.TempDir()
	srv := newTestServer(t)
	p := createProjectForTest(t, srv, dir)

	body := `{"project_id":"` + p.ID + `","posture":"not-a-posture"}`
	req := newAuthedRequest(http.MethodPost, "/v1/sessions", strings.NewReader(body))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
}

func TestCreateSessionRejectsUnsupportedMediaType(t *testing.T) {
	srv := newTestServer(t)
	req := newAuthedRequest(http.MethodPost, "/v1/sessions", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	var response wire.ErrorResponse
	testutil.FailErr(t, "decode response", json.NewDecoder(w.Body).Decode(&response))
	if response.Code != wire.ApiErrorCodeUnsupportedMediaType {
		t.Fatalf("code = %q", response.Code)
	}
}

func TestRequestBodyTooLarge(t *testing.T) {
	dir := t.TempDir()
	srv := newTestServer(t)
	p := createProjectForTest(t, srv, dir)

	padding := strings.Repeat("x", httpio.MaxJSONBody)
	body := `{"project_id":"` + p.ID + `","posture":"spec","pad":"` + padding + `"}`

	req := newAuthedRequest(http.MethodPost, "/v1/sessions", strings.NewReader(body))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
}

func TestInternalErrorSanitized(t *testing.T) {
	srv := newTestServer(t)
	causes := []error{
		errors.New("private database detail"),
		errors.New("credential=fixture-secret\npath=/private/fixture"),
		errors.Join(errors.New("private database detail"), errors.New("fixture-secret")),
		errors.New(`{"error":"fixture-secret","html":"<private>"}`),
		context.Canceled,
		context.DeadlineExceeded,
	}
	var canonical string
	for _, cause := range causes {
		req := newAuthedRequest(http.MethodGet, "/v1/projects", nil)
		w := httptest.NewRecorder()
		srv.responses.InternalError(w, req, cause)
		assertErrorResponse(t, w, http.StatusInternalServerError, "internal_error")
		var response wire.ErrorResponse
		testutil.FailErr(t, "decode internal error", json.Unmarshal(w.Body.Bytes(), &response))
		if response.Title == "" || response.Message == "" {
			t.Fatalf("expected rendered user notice: %+v", response)
		}
		for _, private := range []string{"private database detail", "fixture-secret", "internal server error"} {
			if strings.Contains(w.Body.String(), private) {
				t.Fatalf("response disclosed %q: %s", private, w.Body.String())
			}
		}
		if canonical == "" {
			canonical = w.Body.String()
		} else if w.Body.String() != canonical {
			t.Fatalf("internal cause changed the public response: %s", w.Body.String())
		}
	}
}

func TestCanceledRequestIsNotReportedAsInternalError(t *testing.T) {
	srv := newTestServer(t)
	req := newAuthedRequest(http.MethodGet, "/v1/projects", nil)
	ctx, cancel := context.WithCancel(req.Context())
	cancel()
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req.WithContext(ctx))

	if w.Code != httpio.StatusClientClosedRequest {
		t.Fatalf("status = %d, want %d", w.Code, httpio.StatusClientClosedRequest)
	}
	if w.Body.Len() != 0 {
		t.Fatalf("canceled request body = %q, want empty", w.Body.String())
	}
}
