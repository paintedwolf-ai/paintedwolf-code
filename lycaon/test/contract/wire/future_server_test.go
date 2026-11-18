package contract

import (
	"context"
	"encoding/json"
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
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	wire "github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

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

	var messageID string
	testutil.WaitFor(t, "the prompt turn ends with an assistant message", func() bool {
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
		return messageID != "" && sess.Status == wire.SessionStatusIdle
	})
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
