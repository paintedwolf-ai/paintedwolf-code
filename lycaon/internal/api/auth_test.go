package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGenerateAPIToken(t *testing.T) {
	tok, err := GenerateAPIToken()
	if err != nil {
		t.Fatalf("GenerateAPIToken error: %v", err)
	}
	if len(tok) == 0 {
		t.Fatal("expected non-empty token")
	}
	for _, r := range tok {
		if !((r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_') {
			t.Fatalf("token contains invalid char %q", r)
		}
	}
}

func TestResolveAPIToken(t *testing.T) {
	t.Setenv("LYCAON_API_TOKEN", "env-token-123")
	tok, err := ResolveAPIToken()
	if err != nil {
		t.Fatalf("ResolveAPIToken error: %v", err)
	}
	if tok != "env-token-123" {
		t.Fatalf("token = %q, want env-token-123", tok)
	}

	t.Setenv("LYCAON_API_TOKEN", "")
	tok2, err := ResolveAPIToken()
	if err != nil {
		t.Fatalf("ResolveAPIToken error: %v", err)
	}
	if len(tok2) == 0 {
		t.Fatal("expected generated token when env is empty")
	}
}

func TestBearerTokenFromRequest(t *testing.T) {
	cases := []struct {
		name   string
		header string
		want   string
	}{
		{"standard bearer", "Bearer abc123", "abc123"},
		{"bearer lowercase", "bearer abc123", "abc123"},
		{"no bearer prefix", "abc123", ""},
		{"empty header", "", ""},
		{"bearer with spaces", "Bearer abc 123", "abc 123"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			got := BearerTokenFromRequest(req)
			if got != tc.want {
				t.Fatalf("BearerTokenFromRequest(%q) = %q, want %q", tc.header, got, tc.want)
			}
		})
	}
}

func TestRequireClientAuth(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	srv := NewServer(requiredTestDeps(t, Dependencies{}), nil, "test-secret-token")
	wrapped := srv.requireClientAuth(inner)

	cases := []struct {
		name       string
		authHeader string
		wantStatus int
	}{
		{"valid token", "Bearer test-secret-token", http.StatusOK},
		{"wrong token", "Bearer wrong-token", http.StatusUnauthorized},
		{"no auth header", "", http.StatusUnauthorized},
		{"malformed bearer", "Basic dXNlcjpwYXNz", http.StatusUnauthorized},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/v1/test", nil)
			if tc.authHeader != "" {
				req.Header.Set("Authorization", tc.authHeader)
			}
			w := httptest.NewRecorder()
			wrapped.ServeHTTP(w, req)
			if w.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", w.Code, tc.wantStatus)
			}
		})
	}
}

// An unconfigured token must not mean "serve /v1 to anyone". The port is reachable by
// every same-user process, so a wiring mistake that left the token empty would publish
// the control plane; refusing is the recoverable direction.
func TestRequireClientAuthFailsClosedWhenTokenUnset(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	srv := &Server{}
	wrapped := srv.requireClientAuth(inner)

	req := httptest.NewRequest(http.MethodGet, "/v1/test", nil)
	w := httptest.NewRecorder()
	wrapped.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 when apiToken unset", w.Code)
	}
}

func TestRequireClientAuthWithTestToken(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	srv := NewServer(requiredTestDeps(t, Dependencies{}), nil, TestAPIToken)
	wrapped := srv.requireClientAuth(inner)

	req := httptest.NewRequest(http.MethodGet, "/v1/test", nil)
	req.Header.Set("Authorization", "Bearer "+TestAPIToken)
	w := httptest.NewRecorder()
	wrapped.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
}
