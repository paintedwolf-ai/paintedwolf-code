package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAuthenticatedProfileHandlerRequiresBearerToken(t *testing.T) {
	handler := authenticatedProfileHandler("profile-secret")

	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/debug/pprof/", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", unauthorized.Code)
	}

	req := httptest.NewRequest(http.MethodGet, "/debug/pprof/", nil)
	req.Header.Set("Authorization", "Bearer profile-secret")
	authorized := httptest.NewRecorder()
	handler.ServeHTTP(authorized, req)
	if authorized.Code != http.StatusOK {
		t.Fatalf("authorized status = %d body=%s", authorized.Code, authorized.Body.String())
	}
}

func TestProfileServerRejectsNonLoopbackAndHostnames(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:6060", "localhost:6060", "example.com:6060"} {
		t.Run(addr, func(t *testing.T) {
			t.Setenv(profileAddrEnv, addr)
			app := &ServeApp{APIToken: "profile-secret", resources: newRuntimeResources()}
			err := app.startProfileServer(t.Context())
			if err == nil || !strings.Contains(err.Error(), "loopback IP") {
				t.Fatalf("startProfileServer(%q) = %v, want loopback rejection", addr, err)
			}
		})
	}
}

func TestProfileServerLifecycleUsesOwnedListener(t *testing.T) {
	t.Setenv(profileAddrEnv, "127.0.0.1:0")
	app := &ServeApp{APIToken: "profile-secret", resources: newRuntimeResources()}
	if err := app.startProfileServer(t.Context()); err != nil {
		t.Fatalf("start profile server: %v", err)
	}
	if app.resources.profileServer == nil {
		t.Fatal("profile server was not tracked")
	}
	if err := app.Close(); err != nil {
		t.Fatalf("close profile server: %v", err)
	}
}
