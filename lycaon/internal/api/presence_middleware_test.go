package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
)

func presenceTestServer(t *testing.T) (*Server, *events.Presence) {
	t.Helper()
	hub := events.NewMemoryHub()
	st := store.NewMemory()
	p := events.NewPresence(hub, time.Minute)
	srv := NewServer(requiredTestDeps(t, Dependencies{
		Store: st, Projects: project.NewMemoryRegistry(), Sessions: session.NewManager(st, nil, nil, settings.DefaultSessionLimits()),
		Events: hub, Presence: p,
	}), nil, TestAPIToken)
	return srv, p
}

// presenceRequest issues an authenticated request and fails if auth rejected
// it — an unauthenticated call never reaches the presence middleware, which
// would make "no stamp" assertions pass for the wrong reason.
func presenceRequest(t *testing.T, srv *Server, method, path string) {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	WithTestAuth(req)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code == http.StatusUnauthorized {
		t.Fatalf("%s %s = 401; the request never reached the presence middleware", method, path)
	}
}

// Background polling and streaming do not establish user activity.
func TestPresenceMiddlewareIgnoresReads(t *testing.T) {
	srv, p := presenceTestServer(t)
	presenceRequest(t, srv, http.MethodGet, "/v1/sessions/does-not-exist")
	if got := p.LastUserAction(); !got.IsZero() {
		t.Fatalf("LastUserAction() = %v want zero after a GET", got)
	}
}

// TestPresenceMiddlewareStampsWrites: the stamp lands on the method, before
// routing, so it does not depend on the handler existing or succeeding.
func TestPresenceMiddlewareStampsWrites(t *testing.T) {
	srv, p := presenceTestServer(t)
	presenceRequest(t, srv, http.MethodPost, "/v1/sessions/does-not-exist/prompts")
	if p.LastUserAction().IsZero() {
		t.Fatal("LastUserAction() = zero after a POST")
	}
}

// TestPresenceMiddlewareSkipsUnauthenticated: auth runs first, so a request
// that never proved it came from Den cannot hold background work open.
func TestPresenceMiddlewareSkipsUnauthenticated(t *testing.T) {
	srv, p := presenceTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/sessions", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d want 401", rec.Code)
	}
	if got := p.LastUserAction(); !got.IsZero() {
		t.Fatalf("LastUserAction() = %v want zero for an unauthenticated write", got)
	}
}

// TestPresenceMiddlewareUnwiredIsInert: a server with no presence tracker
// stamps nothing and must not panic.
func TestPresenceMiddlewareUnwiredIsInert(t *testing.T) {
	hub := events.NewMemoryHub()
	st := store.NewMemory()
	srv := NewServer(requiredTestDeps(t, Dependencies{
		Store: st, Projects: project.NewMemoryRegistry(), Sessions: session.NewManager(st, nil, nil, settings.DefaultSessionLimits()),
		Events: hub,
	}), nil, TestAPIToken)
	presenceRequest(t, srv, http.MethodPost, "/v1/sessions/does-not-exist/prompts")
}
