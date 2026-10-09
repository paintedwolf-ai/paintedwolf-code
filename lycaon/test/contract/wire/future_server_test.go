package contract

import (
	"context"
	"encoding/json"
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

func TestRealServerPromptRoutes(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	srv, sessionID := newRealServerWithSession(t, root)

	promptReq, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/sessions/"+sessionID+"/prompts",
		strings.NewReader(`{"operation_id":"00000000-0000-4000-8000-000000000003","text":"hello"}`))
	contractcheck.FailErr(t, "http.NewRequest failed", err)
	promptReq.Header.Set("Content-Type", "application/json")
	api.WithTestAuth(promptReq)
	promptRec := &responseRecorder{header: make(http.Header)}
	srv.ServeHTTP(promptRec, promptReq)
	if promptRec.code != http.StatusAccepted {
		t.Fatalf("prompt status = %d body = %s", promptRec.code, promptRec.body.String())
	}

	deadline := time.Now().Add(10 * time.Second)
	var messageID string
	for time.Now().Before(deadline) {
		getReq, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/sessions/"+sessionID, nil)
		contractcheck.FailErr(t, "http.NewRequest failed", err)
		api.WithTestAuth(getReq)
		getRec := &responseRecorder{header: make(http.Header)}
		srv.ServeHTTP(getRec, getReq)
		if getRec.code != http.StatusOK {
			t.Fatalf("get session status = %d", getRec.code)
		}
		var sess wire.Session
		if err := json.Unmarshal([]byte(getRec.body.String()), &sess); err != nil {
			t.Fatalf("decode session: %v", err)
		}

		msgsReq, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/sessions/"+sessionID+"/messages", nil)
		contractcheck.FailErr(t, "http.NewRequest failed", err)
		api.WithTestAuth(msgsReq)
		msgsRec := &responseRecorder{header: make(http.Header)}
		srv.ServeHTTP(msgsRec, msgsReq)
		if msgsRec.code != http.StatusOK {
			t.Fatalf("list messages status = %d", msgsRec.code)
		}
		var page wire.SessionTranscriptPage
		if err := json.Unmarshal([]byte(msgsRec.body.String()), &page); err != nil {
			t.Fatalf("decode messages: %v", err)
		}
		msgs := page.Messages
		for i := len(msgs) - 1; i >= 0; i-- {
			if msgs[i].Role == wire.MessageRoleAssistant && strings.TrimSpace(msgs[i].ID) != "" {
				messageID = msgs[i].ID
				break
			}
		}
		if messageID != "" && sess.Status == wire.SessionStatusIdle {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if messageID == "" {
		t.Fatal("expected assistant message after prompt turn")
	}
	streamPath := "/v1/sessions/" + sessionID + "/stream?message=" + messageID

	streamReq, err := http.NewRequestWithContext(t.Context(), http.MethodGet, streamPath, nil)
	contractcheck.FailErr(t, "http.NewRequest failed", err)
	api.WithTestAuth(streamReq)
	streamRec := &responseRecorder{header: make(http.Header)}
	srv.ServeHTTP(streamRec, streamReq)
	if streamRec.code != http.StatusOK {
		t.Fatalf("stream status = %d", streamRec.code)
	}
	if ct := streamRec.header.Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("content-type = %q", ct)
	}
	if !strings.Contains(streamRec.body.String(), `"done":true`) {
		t.Fatalf("stream body = %s", streamRec.body.String())
	}
}

func newRealServerWithSession(t *testing.T, root string) (*api.Server, string) {
	t.Helper()
	store := store.NewMemory()
	mockCfg, err := llm.LoadMockConfig()
	if err != nil {
		t.Fatalf("load mock config: %v", err)
	}
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())
	mgr := session.NewManager(store, llm.NewMockProvider(mockCfg), tools.NewStubRegistry(), settings.DefaultSessionLimits())
	srv := api.NewServer(apitest.Dependencies(t, api.Dependencies{Core:api.CoreDependencies{Store: store, Projects: project.NewMemoryRegistry(), Sessions: mgr,},}), nil, api.TestAPIToken)

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
