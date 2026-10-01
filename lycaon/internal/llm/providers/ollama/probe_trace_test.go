package ollama

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/testutil"
)

// The residency and context probes that precede a chat request are not
// request attempts: the request's transport trace sees only the chat
// connection.
func TestOllamaProbesStayOutsideTheRequestTrace(t *testing.T) {
	var probes, chats atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/ps":
			probes.Add(1)
			_, _ = w.Write([]byte(`{"models":[]}`))
		case "/api/show":
			probes.Add(1)
			_, _ = w.Write([]byte(`{"model_info":{"gemma4.context_length":131072}}`))
		case "/api/chat":
			chats.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"message": map[string]any{"role": "assistant", "content": "ok"},
				"done":    true,
			})
		default:
			t.Errorf("unexpected call to %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	var traced atomic.Int32
	ctx := httptrace.WithClientTrace(t.Context(), &httptrace.ClientTrace{
		GetConn: func(string) { traced.Add(1) },
	})
	// No catalog context length, so the adapter also probes /api/show.
	p := New("ollama-1", srv.URL+"/v1", "", []modelinfo.Entry{{ID: "gemma4:e2b"}})
	_, err := p.Complete(ctx, smallRequest("gemma4:e2b"))
	testutil.FailErr(t, "complete", err)
	if chats.Load() != 1 || probes.Load() == 0 {
		t.Fatalf("chats = %d probes = %d, want one chat behind at least one probe", chats.Load(), probes.Load())
	}
	if traced.Load() != 1 {
		t.Fatalf("traced connections = %d, want only the chat request", traced.Load())
	}
}
