package api

import (
	"context"
	"log"
	"net/http"
	"runtime/debug"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/lycaon/lycaon/internal/observability"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// recoverHTTPPanics keeps the public transport contract JSON-shaped even when
// a handler violates its internal contract.
func (s *Server) recoverHTTPPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func(ctx context.Context) {
			if recovered := recover(); recovered != nil {
				s.responses.Logger.ErrorContext(ctx, "http handler panic",
					"panic", recovered,
					"path", r.URL.Path,
					"method", r.Method,
					"stack", string(debug.Stack()))
				s.responses.Fail(w, wire.ApiErrorCodeInternalError, "internal server error")
			}
		}(r.Context())
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleRouteNotFound(w http.ResponseWriter, _ *http.Request) {
	s.responses.Fail(w, wire.ApiErrorCodeNotFound, "route not found")
}

func (s *Server) handleMethodNotAllowed(w http.ResponseWriter, r *http.Request) {
	setAllowHeader(w.Header(), allowedMethodsForPath(r.URL.Path))
	s.responses.Fail(w, wire.ApiErrorCodeMethodNotAllowed, "method not allowed")
}

func allowedMethodsForPath(path string) []string {
	seen := make(map[string]struct{})
	for _, operation := range allGeneratedOperations {
		if operationPathMatches(operation.Path, path) {
			seen[operation.Method] = struct{}{}
		}
	}
	methods := make([]string, 0, len(seen))
	for method := range seen {
		methods = append(methods, method)
	}
	sort.Strings(methods)
	return methods
}

func operationPathMatches(pattern, path string) bool {
	patternParts := strings.Split(strings.Trim(pattern, "/"), "/")
	pathParts := strings.Split(strings.Trim(path, "/"), "/")
	if len(patternParts) != len(pathParts) {
		return false
	}
	for i, patternPart := range patternParts {
		if strings.HasPrefix(patternPart, "{") && strings.HasSuffix(patternPart, "}") && pathParts[i] != "" {
			continue
		}
		if patternPart != pathParts[i] {
			return false
		}
	}
	return true
}

func setAllowHeader(header http.Header, methods []string) {
	if len(methods) == 0 {
		header.Del("Allow")
		return
	}
	header.Set("Allow", strings.Join(methods, ", "))
}

// requestLogger writes the access line to stderr through the non-blocking
// console sink; stdout is reserved for the startup protocol.
func requestLogger() func(http.Handler) http.Handler {
	return middleware.RequestLogger(&middleware.DefaultLogFormatter{
		Logger:  log.New(observability.ConsoleStderr(), "", log.LstdFlags),
		NoColor: true,
	})
}
