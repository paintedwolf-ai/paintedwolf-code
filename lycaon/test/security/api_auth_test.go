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
	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	wire "github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
)

// TestBearerTokenAdmission checks what each credential earns on one server:
// none and a wrong one are refused with the declared error, the right one is
// served, health needs none, and a session request naming a directory is
// refused at decode. Every operation's refusal is swept by TestAPIConformance.
func TestBearerTokenAdmission(t *testing.T) {
	srv := wiring.BuildForTest(t).Server
	sessID := "00000000-0000-4000-8000-000000000001"
	for _, tc := range []struct {
		name, method, path, authorization, body string
		status                                  int
		code                                    wire.ApiErrorCode
		openAPIPath                             string
		params                                  map[string]string
	}{
		{name: "no token", method: http.MethodGet, path: "/v1/projects", status: http.StatusUnauthorized, code: wire.ApiErrorCodeUnauthorized},
		{name: "no token on a session matches the declared error", method: http.MethodGet, path: "/v1/sessions/" + sessID,
			status: http.StatusUnauthorized, code: wire.ApiErrorCodeUnauthorized, openAPIPath: "/v1/sessions/{id}", params: map[string]string{"id": sessID}},
		{name: "wrong token", method: http.MethodGet, path: "/v1/projects", authorization: "Bearer wrong-token", status: http.StatusUnauthorized, code: wire.ApiErrorCodeUnauthorized},
		{name: "valid token", method: http.MethodGet, path: "/v1/projects", authorization: api.TestAuthHeader(), status: http.StatusOK},
		{name: "health needs no token", method: http.MethodGet, path: "/health", status: http.StatusOK},
		{name: "a session names no project directory", method: http.MethodPost, path: "/v1/sessions", authorization: api.TestAuthHeader(),
			body: `{"project_dir":"/tmp","posture":"build"}`, status: http.StatusBadRequest, code: wire.ApiErrorCodeInvalidJson},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var body io.Reader
			if tc.body != "" {
				body = strings.NewReader(tc.body)
			}
			req := httptest.NewRequestWithContext(t.Context(), tc.method, tc.path, body)
			if tc.authorization != "" {
				req.Header.Set("Authorization", tc.authorization)
			}
			if tc.body != "" {
				req.Header.Set("Content-Type", "application/json")
			}
			w := httptest.NewRecorder()
			srv.ServeHTTP(w, req)
			if w.Code != tc.status {
				t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
			}
			if tc.code != "" {
				if got := decodeAPIError(t, w).Code; got != tc.code {
					t.Fatalf("code = %q, want %q", got, tc.code)
				}
			}
			if tc.openAPIPath != "" {
				AssertResponseMatchesOpenAPI(t, w, tc.method, tc.openAPIPath, tc.params)
			}
		})
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

func TestResolveAPITokenFromEnv(t *testing.T) {
	t.Setenv("LYCAON_API_TOKEN", "env-token-123")
	got, err := api.ResolveAPIToken()
	testutil.FailErr(t, "api.ResolveAPIToken failed", err)
	if got != "env-token-123" {
		t.Fatalf("token = %q", got)
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
		name              string
		path              string
		expectActionBlock bool
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
			reject := tools.ValidateAttachedRootsForAction([]string{tc.path})
			if tc.expectActionBlock && reject == nil {
				t.Fatalf("%s (%s): expected ValidateAttachedRootsForAction to refuse execution root", tc.name, tc.path)
			}
		})
	}
}
