package ollama

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestPreferResidentContext(t *testing.T) {
	cases := map[string]struct {
		bucket, need, resident, maxContext, want int
	}{
		"nothing loaded":            {bucket: 8192, need: 3000, resident: 0, maxContext: 131072, want: 8192},
		"larger runner holds it":    {bucket: 8192, need: 3000, resident: 16384, maxContext: 131072, want: 16384},
		"exact fit":                 {bucket: 16384, need: 16384, resident: 16384, maxContext: 131072, want: 16384},
		"runner too small grows":    {bucket: 16384, need: 12000, resident: 8192, maxContext: 131072, want: 16384},
		"runner beyond model limit": {bucket: 8192, need: 3000, resident: 65536, maxContext: 32768, want: 8192},
		"unknown model limit":       {bucket: 8192, need: 3000, resident: 16384, maxContext: 0, want: 16384},
	}
	for name, tc := range cases {
		if got := preferResidentContext(tc.bucket, tc.need, tc.resident, tc.maxContext); got != tc.want {
			t.Fatalf("%s: num_ctx = %d want %d", name, got, tc.want)
		}
	}
}

// residencyServer answers /api/ps with the given runners and records the
// num_ctx of each chat request.
func residencyServer(t *testing.T, ps string, psStatus int) (*httptest.Server, *[]int) {
	t.Helper()
	var sent []int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/ps":
			w.WriteHeader(psStatus)
			_, _ = w.Write([]byte(ps))
		case "/api/chat":
			var body Request
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode chat body: %v", err)
			}
			sent = append(sent, body.Options.NumCtx)
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
	return srv, &sent
}

func smallRequest(model string) modelcall.CompletionRequest {
	return modelcall.CompletionRequest{
		Model: model, MaxTokens: 256,
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "name this chat"}},
	}
}

func TestOllamaRequestKeepsResidentRunnerContext(t *testing.T) {
	cases := map[string]struct {
		model    string
		ps       string
		psStatus int
		want     int
	}{
		"larger runner loaded": {
			model:    "gemma4:e2b",
			ps:       `{"models":[{"name":"gemma4:e2b","model":"gemma4:e2b","context_length":16384}]}`,
			psStatus: http.StatusOK,
			want:     16384,
		},
		"implicit latest tag": {
			model:    "gemma4",
			ps:       `{"models":[{"name":"gemma4:latest","model":"gemma4:latest","context_length":32768}]}`,
			psStatus: http.StatusOK,
			want:     32768,
		},
		"another model loaded": {
			model:    "gemma4:e2b",
			ps:       `{"models":[{"name":"qwen3:8b","model":"qwen3:8b","context_length":32768}]}`,
			psStatus: http.StatusOK,
			want:     8192,
		},
		"nothing loaded": {
			model:    "gemma4:e2b",
			ps:       `{"models":[]}`,
			psStatus: http.StatusOK,
			want:     8192,
		},
		"listing unavailable": {
			model:    "gemma4:e2b",
			ps:       `{"error":"boom"}`,
			psStatus: http.StatusInternalServerError,
			want:     8192,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			srv, sent := residencyServer(t, tc.ps, tc.psStatus)
			p := New("ollama-1", srv.URL+"/v1", "", []modelinfo.Entry{{ID: tc.model, ContextLength: 131072}})
			_, err := p.Complete(t.Context(), smallRequest(tc.model))
			testutil.FailErr(t, "complete", err)
			if len(*sent) != 1 || (*sent)[0] != tc.want {
				t.Fatalf("num_ctx sent = %v want [%d]", *sent, tc.want)
			}
		})
	}
}

func TestOllamaRequestGrowsPastSmallerResidentRunner(t *testing.T) {
	srv, sent := residencyServer(t, `{"models":[{"name":"gemma4:e2b","model":"gemma4:e2b","context_length":8192}]}`, http.StatusOK)
	p := New("ollama-1", srv.URL+"/v1", "", []modelinfo.Entry{{ID: "gemma4:e2b", ContextLength: 131072}})
	req := smallRequest("gemma4:e2b")
	req.Messages[0].Content = strings.Repeat("a", 32000) // generic estimate: 8000
	_, err := p.Complete(t.Context(), req)
	testutil.FailErr(t, "complete", err)
	if len(*sent) != 1 || (*sent)[0] <= 8192 {
		t.Fatalf("num_ctx sent = %v want a bucket above the resident 8192", *sent)
	}
}
