package api

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/lycaon/lycaon/internal/people"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// TestAPIToken is the bearer token used by in-process and httptest helpers.
//
//nolint:gosec // G101: this fixed bearer is restricted to development and tests.
const TestAPIToken = "lycaon-test-token"

// ResolveAPIToken returns the API bearer token from LYCAON_API_TOKEN or generates one.
func ResolveAPIToken() (string, error) {
	if t := strings.TrimSpace(os.Getenv("LYCAON_API_TOKEN")); t != "" {
		return t, nil
	}
	return GenerateAPIToken()
}

// GenerateAPIToken creates a URL-safe random bearer token.
func GenerateAPIToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate api token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// BearerTokenFromRequest extracts the token from Authorization: Bearer <token>.
func BearerTokenFromRequest(r *http.Request) string {
	h := strings.TrimSpace(r.Header.Get("Authorization"))
	if !strings.HasPrefix(strings.ToLower(h), "bearer ") {
		return ""
	}
	return strings.TrimSpace(h[7:])
}

func (s *Server) requireClientAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.apiToken == "" {
			// An unset token disables authenticated routes.
			s.responses.Fail(w, wire.ApiErrorCodeAuthUnavailable, "api token is not configured")
			return
		}
		got := BearerTokenFromRequest(r)
		// Equal-length token comparisons do not expose matching prefixes through timing.
		if got == "" || subtle.ConstantTimeCompare([]byte(got), []byte(s.apiToken)) != 1 {
			s.responses.Fail(w, wire.ApiErrorCodeUnauthorized, "missing or invalid bearer token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// bindCaller resolves the authenticated credential to the person it belongs
// to. The device credential belongs to the host owner.
func (s *Server) bindCaller(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		owner, err := s.sessionStore.HostOwner(r.Context())
		if err != nil {
			s.responses.InternalError(w, r, err)
			return
		}
		next.ServeHTTP(w, r.WithContext(people.WithCaller(r.Context(), owner)))
	})
}

// authorizeOperation admits a request only when its caller may invoke operation.
func (s *Server) authorizeOperation(operation generatedOperation, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		caller, ok := people.Caller(r.Context())
		if !ok || !people.MayInvoke(caller, operation.ID) {
			s.responses.Fail(w, wire.ApiErrorCodeForbidden, "caller may not invoke "+operation.ID)
			return
		}
		next(w, r)
	}
}
