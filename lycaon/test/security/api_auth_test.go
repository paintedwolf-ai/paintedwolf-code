package security

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/test/wiring"
)

func TestV1RequiresBearerToken(t *testing.T) {
	srv := wiring.BuildForTest(t).Server

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/projects", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	errResp := decodeAPIError(t, w)
	if errResp.Code != "unauthorized" {
		t.Fatalf("code = %q", errResp.Code)
	}
}

func TestUnauthenticatedGetSessionMatchesOpenAPIError(t *testing.T) {
	srv := wiring.BuildForTest(t).Server
	sessID := "00000000-0000-4000-8000-000000000001"
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/sessions/"+sessID, nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	AssertResponseMatchesOpenAPI(t, w, http.MethodGet, "/v1/sessions/{id}", map[string]string{"id": sessID})
}

func TestV1WrongBearerToken(t *testing.T) {
	srv := wiring.BuildForTest(t).Server

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/projects", nil)
	req.Header.Set("Authorization", "Bearer wrong-token")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", w.Code)
	}
}

func TestV1ValidBearerToken(t *testing.T) {
	srv := wiring.BuildForTest(t).Server

	req := authedRequest(t, http.MethodGet, "/v1/projects", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
}

func TestHealthExemptFromAuth(t *testing.T) {
	srv := wiring.BuildForTest(t).Server

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
}

func TestResolveAPITokenFromEnv(t *testing.T) {
	t.Setenv("LYCAON_API_TOKEN", "env-token-123")
	got, err := api.ResolveAPIToken()
	testutil.FailErr(t, "api.ResolveAPIToken failed", err)
	if got != "env-token-123" {
		t.Fatalf("token = %q", got)
	}
}

func TestV1RoutesRequireAuthTable(t *testing.T) {
	srv := wiring.BuildForTest(t).Server
	sessID := "00000000-0000-4000-8000-000000000001"

	routes := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/v1/projects", ""},
		{http.MethodPost, "/v1/sessions", `{"project_id":"00000000-0000-4000-8000-000000000001","posture":"build"}`},
		{http.MethodPost, "/v1/projects", `{"roots":[{"path":"/tmp"}]}`},
		{http.MethodGet, "/v1/sessions/" + sessID, ""},
		{http.MethodGet, "/v1/settings/model-policy", ""},
		{http.MethodGet, "/v1/settings/approvals", ""},
		{http.MethodGet, "/v1/cost/summary", ""},
		{http.MethodPatch, "/v1/settings/model-policy", `{}`},
	}

	for _, route := range routes {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			var body io.Reader
			if route.body != "" {
				body = strings.NewReader(route.body)
			}
			req := httptest.NewRequestWithContext(t.Context(), route.method, route.path, body)
			if route.body != "" {
				req.Header.Set("Content-Type", "application/json")
			}
			w := httptest.NewRecorder()
			srv.ServeHTTP(w, req)
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestCreateSessionRejectsProjectDir(t *testing.T) {
	srv := wiring.BuildForTest(t).Server
	body := `{"project_dir":"/tmp","posture":"build"}`
	req := authedRequest(t, http.MethodPost, "/v1/sessions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	errResp := decodeAPIError(t, w)
	// CreateSessionRequest has no project_dir; DisallowUnknownFields rejects it
	// at decode time as invalid_json, not invalid_request.
	if errResp.Code != "invalid_json" {
		t.Fatalf("code = %q", errResp.Code)
	}
}

func TestCreateProjectAllowsUserSelectedPath(t *testing.T) {
	dir := t.TempDir()
	project.SetDefaultOpenPolicy(project.DefaultOpenPolicy())
	t.Cleanup(func() {
		project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	})

	srv := wiring.BuildForTest(t).Server
	req := authedRequest(t, http.MethodPost, "/v1/projects", strings.NewReader(`{"roots":[{"path":"`+dir+`"}]}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
}

func TestCreateProjectDeniesSSH(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, path := range []string{filepath.Join(home, ".ssh"), filepath.Join(home, "Library", "Keychains")} {
		testutil.FailErr(t, "create protected store fixture", os.MkdirAll(path, 0o700))
	}
	srv := wiring.BuildForTest(t).Server
	// Use production-style deny rules for this case.
	project.SetDefaultOpenPolicy(project.DefaultOpenPolicy())
	t.Cleanup(func() { project.SetDefaultOpenPolicy(project.TestOpenPolicy()) })

	attach := func(t *testing.T, path string) (int, string) {
		t.Helper()
		req := authedRequest(t, http.MethodPost, "/v1/projects", strings.NewReader(`{"roots":[{"path":"`+path+`"}]}`))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		if w.Code >= 200 && w.Code < 300 {
			return w.Code, ""
		}
		return w.Code, string(decodeAPIError(t, w).Code)
	}

	// Existing catalogued stores are refused without relying on machine contents.
	cases := []struct {
		name string
		path string
		why  string
	}{
		{"the store itself", filepath.Join(home, ".ssh"), "private keys"},
		{"credential store", filepath.Join(home, "Library", "Keychains"), "credential store"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, code := attach(t, tc.path)
			if status != http.StatusBadRequest || code != "write_root_under_secret_store" {
				t.Fatalf("%s (%s): status = %d code = %q, want 400 write_root_under_secret_store",
					tc.path, tc.why, status, code)
			}
		})
	}

	t.Run("store ancestor still attaches", func(t *testing.T) {
		status, code := attach(t, filepath.Join(home, "Library"))
		if status != http.StatusCreated {
			t.Fatalf("store ancestor: status = %d code = %q, want 201", status, code)
		}
		status, code = attach(t, filepath.Join(home, "Library", "Keychains"))
		if status != http.StatusBadRequest || code != "write_root_under_secret_store" {
			t.Fatalf("protected descendant after ancestor attach: status = %d code = %q", status, code)
		}
	})

	// Positive control. A suite that passes because every attach is refused has
	// proved nothing about the predicate.
	t.Run("ordinary folder still attaches", func(t *testing.T) {
		status, code := attach(t, t.TempDir())
		if status != http.StatusCreated {
			t.Fatalf("ordinary folder: status = %d code = %q, want 201", status, code)
		}
	})
}
