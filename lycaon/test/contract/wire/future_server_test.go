package contract

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/api/apitest"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/tools"
	wire "github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/wirespec"
)

func TestRealServerRouteCoverage(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	routes, err := wirespec.LoadOpenAPIRoutes(root)
	contractcheck.FailErr(t, "load OpenAPI routes from docs/openapi.yaml", err)

	srv, sessionID := newRealServerWithSession(t, root)
	implemented := map[string]struct{}{
		"GET /health": {},
	}

	var pending []string
	for _, route := range routes {
		key := route.Method + " " + route.Path
		url := stubURL("http://example.com", route.Method, route.Path)
		url = strings.ReplaceAll(url, fixtureSessionID, sessionID)
		url = strings.TrimPrefix(url, "http://example.com")

		var body io.Reader
		if route.Method == http.MethodPost || route.Method == http.MethodPut {
			body = strings.NewReader(routeProbeBody(route.Path))
		}

		req, err := http.NewRequestWithContext(t.Context(), route.Method, url, body)
		contractcheck.FailErr(t, "http.NewRequest failed", err)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if route.Path != "/health" {
			api.WithTestAuth(req)
		}
		rec := &responseRecorder{header: make(http.Header)}
		srv.ServeHTTP(rec, req)

		if rec.code == http.StatusNotFound && !isApplicationNotFound(rec.body.String()) {
			pending = append(pending, key)
			continue
		}
		implemented[key] = struct{}{}
	}

	t.Logf("real server routes implemented: %d (including /health)", len(implemented))
	t.Logf("contract routes pending on real server: %d", len(pending))
	for _, p := range pending {
		t.Logf("  pending: %s", p)
	}

	if _, ok := implemented["GET /health"]; !ok {
		t.Fatal("real server must expose GET /health")
	}
	if _, ok := implemented["POST /v1/sessions/{id}/prompts"]; !ok {
		t.Fatal("real server must expose POST /v1/sessions/{id}/prompts")
	}
	if _, ok := implemented["GET /v1/sessions/{id}/stream"]; !ok {
		t.Fatal("real server must expose GET /v1/sessions/{id}/stream")
	}
}

func newRealServerWithSession(t *testing.T, root string) (*api.Server, string) {
	t.Helper()
	store := store.NewMemory()
	mockCfg, err := llm.LoadMockConfig()
	if err != nil {
		t.Fatalf("load mock config: %v", err)
	}
	mgr := session.NewHost(store, session.Models{Client: llm.NewMockProvider(mockCfg), Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	srv := api.NewServer(apitest.Dependencies(t, api.Dependencies{Core: api.CoreDependencies{Store: store, Projects: project.NewMemoryRegistry(), Sessions: mgr}}), nil, api.TestAPIToken)

	// Wait for detached prompt work before temporary-directory cleanup.
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		srv.WaitForBackground(ctx)
	})
	sess, err := store.Create(context.Background(), wire.CreateSessionRequest{
		Posture: wire.SessionPostureBuild,
	}, testdbseed.DefaultProjectID)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	return srv, sess.ID
}

func routeProbeBody(path string) string {
	switch {
	case strings.HasSuffix(path, "/prompts"):
		return `{"text":"probe"}`
	case strings.HasSuffix(path, "/open"):
		return `{"path":"/tmp"}`
	case strings.Contains(path, "/tools/"):
		return `{"args":{}}`
	default:
		return `{}`
	}
}

// isApplicationNotFound distinguishes chi router 404 from handler-level not-found JSON.
func isApplicationNotFound(body string) bool {
	return strings.Contains(body, `"code"`) || strings.Contains(body, "session not found") ||
		strings.Contains(body, "message_not_found") || strings.Contains(body, "project not found")
}

type responseRecorder struct {
	code   int
	header http.Header
	body   strings.Builder
}

func (r *responseRecorder) Header() http.Header { return r.header }

func (r *responseRecorder) Write(b []byte) (int, error) {
	if r.code == 0 {
		r.code = http.StatusOK
	}
	return r.body.Write(b)
}

func (r *responseRecorder) WriteHeader(statusCode int) { r.code = statusCode }

func (r *responseRecorder) Flush() {}
