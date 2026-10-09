package hostcontracts

import (
	"net/http"
	"net/http/httptest"
	"testing"

	hostapi "github.com/lycaon/lycaon/internal/api"
	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
)

func TestPresenceMiddlewareIgnoresReads(t *testing.T) {
	srv, p := contractfixture.PresenceTestServer(t)
	contractfixture.PresenceRequest(t, srv, http.MethodGet, "/v1/sessions/does-not-exist")
	if got := p.LastUserAction(); !got.IsZero() {
		t.Fatalf("LastUserAction() = %v want zero after a GET", got)
	}
}

// TestPresenceMiddlewareStampsWrites: the stamp lands on the method, before
// routing, so it does not depend on the handler existing or succeeding.

func TestPresenceMiddlewareStampsWrites(t *testing.T) {
	srv, p := contractfixture.PresenceTestServer(t)
	contractfixture.PresenceRequest(t, srv, http.MethodPost, "/v1/sessions/does-not-exist/prompts")
	if p.LastUserAction().IsZero() {
		t.Fatal("LastUserAction() = zero after a POST")
	}
}

// TestPresenceMiddlewareSkipsUnauthenticated: auth runs first, so a request
// that never proved it came from Den cannot hold background work open.

func TestPresenceMiddlewareSkipsUnauthenticated(t *testing.T) {
	srv, p := contractfixture.PresenceTestServer(t)
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
	srv := hostapi.NewServer(contractfixture.RequiredTestDeps(t, hostapi.Dependencies{Core: hostapi.CoreDependencies{
		Store: st, Projects: project.NewMemoryRegistry(), Sessions: session.NewHost(st, session.Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, nil)}, Host: hostapi.HostDependencies{
		Events: hub}}), nil, hostapi.TestAPIToken)
	contractfixture.PresenceRequest(t, srv, http.MethodPost, "/v1/sessions/does-not-exist/prompts")
}
