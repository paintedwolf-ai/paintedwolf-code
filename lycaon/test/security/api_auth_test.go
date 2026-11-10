package security

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
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

// The desktop reports stepping away, including input idle, through one lock
// route; unlock lifetime reasons that do not describe leaving are refused.
func TestVaultLockAcceptsStepAwayReasonsOnly(t *testing.T) {
	srv := wiring.BuildForTest(t).Server
	for _, reason := range []string{"screen_locked", "sleep", "away", "app_quit"} {
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, authedRequest(t, http.MethodPost, "/v1/vault/lock", strings.NewReader(`{"reason":"`+reason+`"}`)))
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"locked":`) {
			t.Fatalf("%s: status %d body %s", reason, w.Code, w.Body.String())
		}
		AssertResponseMatchesOpenAPI(t, w, http.MethodPost, "/v1/vault/lock", nil)
	}
	for _, reason := range []string{"manual", "idle", "ceiling", "restart", "elsewhere"} {
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, authedRequest(t, http.MethodPost, "/v1/vault/lock", strings.NewReader(`{"reason":"`+reason+`"}`)))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s: status %d body %s", reason, w.Code, w.Body.String())
		}
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

	srv := wiring.BuildForTest(t).Server
	req := authedRequest(t, http.MethodPost, "/v1/projects", strings.NewReader(`{"roots":[{"path":"`+dir+`"}]}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
}

func TestCreateProjectAllowsAnyExistingDirectoryWhileActionGuardStaysFailClosed(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	for _, path := range []string{
		filepath.Join(home, ".config"),
		filepath.Join(home, ".ssh"),
		filepath.Join(home, "Library", "Keychains"),
	} {
		testutil.FailErr(t, "create fixture directory", os.MkdirAll(path, 0o700))
	}
	srv := wiring.BuildForTest(t).Server
	cfgRoot, err := configdir.Root()
	testutil.FailErr(t, "resolve configdir root", err)
	testutil.FailErr(t, "create configdir root", os.MkdirAll(cfgRoot, 0o700))

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

	cases := []struct {
		name               string
		path               string
		expectWriteExcluded bool
	}{
		{"home directory", home, false},
		{"dot-config directory", filepath.Join(home, ".config"), false},
		{"ssh directory", filepath.Join(home, ".ssh"), true},
		{"credential store", filepath.Join(home, "Library", "Keychains"), true},
		{"device config root", cfgRoot, true},
		{"ordinary folder", t.TempDir(), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, code := attach(t, tc.path)
			if status != http.StatusCreated {
				t.Fatalf("%s (%s): status = %d code = %q, want 201 Created", tc.name, tc.path, status, code)
			}
			if reject := tools.ValidateAttachedRootsForAction([]string{tc.path}); reject != nil {
				t.Fatalf("%s (%s): ValidateAttachedRootsForAction refused valid directory: %+v", tc.name, tc.path, reject)
			}
			writeRoots, err := confine.WriteRootsForBoundary("", []string{tc.path}, nil, "")
			testutil.FailErr(t, "write roots for boundary", err)
			if tc.expectWriteExcluded && slices.Contains(writeRoots, fspath.CanonicalPath(tc.path)) {
				t.Fatalf("%s (%s): protected/control-plane root admitted into executable write roots: %v", tc.name, tc.path, writeRoots)
			}
		})
	}
}
