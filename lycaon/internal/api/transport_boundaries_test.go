package api

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/testutil"
)

type transportRoute struct {
	method string
	path   string
}

func TestVersionedRoutesPreserveTransportBoundaries(t *testing.T) {
	t.Setenv("LYCAON_DEV_CORS", "0")
	root := t.TempDir()
	recovery := NewRecoveryServer(t.Context(), RecoveryServerOpts{
		DataDir: root, DBPath: filepath.Join(root, "store.db"), APIToken: TestAPIToken,
	})
	// The recovery constructor takes no notice catalog; bind the bundled one.
	recovery.responses.Notices = testUserNotices(t)
	normalRoot := t.TempDir()
	normal := newTestServer(t, func(d *Dependencies) {
		d.Storage.DataDir = normalRoot
		d.Storage.StorePath = filepath.Join(normalRoot, "store.db")
	})
	for name, server := range map[string]*Server{"normal": normal, "recovery": recovery} {
		t.Run(name, func(t *testing.T) {
			routes := versionedTransportRoutes(t, server)
			for _, pending := range []bool{false, true} {
				phase := "ready"
				if pending {
					phase = "restore pending"
					server.markRestorePending()
				}
				t.Run(phase, func(t *testing.T) {
					for _, route := range routes {
						t.Run(route.method+" "+route.path, func(t *testing.T) {
							for _, origin := range productionCORSOrigins {
								assertRouteTransport(t, server, route, origin, pending)
							}
						})
					}
				})
			}
		})
	}
}

func versionedTransportRoutes(t *testing.T, server *Server) []transportRoute {
	t.Helper()
	parameter := regexp.MustCompile(`\{[^{}]+\}`)
	var routes []transportRoute
	err := chi.Walk(server.router, func(method, path string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if strings.HasPrefix(path, "/v1/") {
			routes = append(routes, transportRoute{
				method: method,
				path:   parameter.ReplaceAllString(path, "00000000-0000-4000-8000-000000000001"),
			})
		}
		return nil
	})
	testutil.FailErr(t, "walk versioned routes", err)
	if len(routes) == 0 {
		t.Fatal("server registered no versioned routes")
	}
	return routes
}

func assertRouteTransport(t *testing.T, server *Server, route transportRoute, origin string, pending bool) {
	t.Helper()
	for _, authorization := range []string{"", "Bearer invalid-token"} {
		r := httptest.NewRequestWithContext(t.Context(), route.method, route.path, nil)
		r.Header.Set("Origin", origin)
		if authorization != "" {
			r.Header.Set("Authorization", authorization)
		}
		w := httptest.NewRecorder()
		server.ServeHTTP(w, r)
		assertErrorResponse(t, w, http.StatusUnauthorized, "unauthorized")
		assertReadableTransportError(t, w, origin)
	}
	if pending {
		r := newAuthedRequest(route.method, route.path, nil)
		r.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		server.ServeHTTP(w, r)
		assertErrorResponse(t, w, http.StatusConflict, "backup_restore_pending")
		assertReadableTransportError(t, w, origin)
	}
	assertRoutePreflight(t, server, route, origin)
}

func assertReadableTransportError(t *testing.T, w *httptest.ResponseRecorder, origin string) {
	t.Helper()
	if got := w.Header().Get("Content-Type"); got != httpio.MediaTypeJSON {
		t.Fatalf("error media type = %q", got)
	}
	if got := strings.Join(w.Header().Values("Access-Control-Allow-Origin"), ", "); got != origin {
		t.Fatalf("response origin = %q, want %q", got, origin)
	}
	assertCORSHeaderAccess(t, w, "Access-Control-Expose-Headers", "Content-Disposition", "X-Export-Truncated", "ETag", "Retry-After")
}

func assertRoutePreflight(t *testing.T, server *Server, route transportRoute, origin string) {
	t.Helper()
	r := httptest.NewRequestWithContext(t.Context(), http.MethodOptions, route.path, nil)
	r.Header.Set("Origin", origin)
	r.Header.Set("Access-Control-Request-Method", route.method)
	r.Header.Set("Access-Control-Request-Headers", "authorization,content-type")
	w := httptest.NewRecorder()
	server.ServeHTTP(w, r)
	if w.Code < http.StatusOK || w.Code >= http.StatusMultipleChoices || strings.Join(w.Header().Values("Access-Control-Allow-Origin"), ", ") != origin {
		t.Fatalf("preflight status=%d origin=%q body=%s", w.Code, w.Header().Get("Access-Control-Allow-Origin"), w.Body.String())
	}
	assertCORSHeaderAccess(t, w, "Access-Control-Allow-Headers", "Authorization", "Content-Type")
	methods := strings.Join(w.Header().Values("Access-Control-Allow-Methods"), ",")
	for _, method := range strings.Split(methods, ",") {
		if strings.TrimSpace(method) == route.method {
			return
		}
	}
	t.Fatalf("preflight methods = %q, missing %q", methods, route.method)
}

func assertCORSHeaderAccess(t *testing.T, w *httptest.ResponseRecorder, field string, required ...string) {
	t.Helper()
	names := make(map[string]bool)
	for _, value := range w.Header().Values(field) {
		for _, name := range strings.Split(value, ",") {
			names[http.CanonicalHeaderKey(strings.TrimSpace(name))] = true
		}
	}
	for _, name := range required {
		if !names[http.CanonicalHeaderKey(name)] {
			t.Errorf("%s does not include %s", field, name)
		}
	}
}
