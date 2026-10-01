package vertexexpress

import (
	"context"
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

func TestVertexExpressDriverConformance(t *testing.T) {
	const apiKey = "conformance-key"
	var gotKey, gotAuthz, gotPath, gotQuery string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("x-goog-api-key")
		gotAuthz = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		resp := vertexExpressResponse{
			Candidates: []vertexExpressCandidate{{
				Content:      vertexExpressContent{Role: "model", Parts: []vertexExpressPart{{Text: "pong"}}},
				FinishReason: "STOP",
			}},
			UsageMetadata: &vertexExpressUsageMetadata{PromptTokenCount: 4, CandidatesTokenCount: 2},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)

	provider := New("vertex-express", srv.URL, apiKey, []modelinfo.Entry{{ID: "gemini-2.5-flash"}})
	completion, err := provider.Complete(context.Background(), modelcall.CompletionRequest{
		Model: "gemini-2.5-flash",
		Messages: []api.Message{{
			Role:    api.MessageRoleUser,
			Content: "ping",
		}},
	})
	testutil.FailErr(t, "Complete", err)
	if completion.Content != "pong" {
		t.Fatalf("content = %q, want %q", completion.Content, "pong")
	}
	if gotKey != apiKey {
		t.Errorf("x-goog-api-key = %q, want %q", gotKey, apiKey)
	}
	// The key must not also travel as a bearer token or a query parameter —
	// express mode authenticates with the API-key header alone.
	if gotAuthz != "" {
		t.Errorf("Authorization = %q, want no bearer header", gotAuthz)
	}
	if strings.Contains(gotQuery, "key=") {
		t.Errorf("query = %q, want the key out of the URL", gotQuery)
	}
	// Express mode takes no project or location: the model resource hangs off
	// the global host under publishers/google.
	if want := "/publishers/google/models/gemini-2.5-flash:generateContent"; gotPath != want {
		t.Errorf("path = %q, want %q", gotPath, want)
	}
	if strings.Contains(gotPath, "/projects/") || strings.Contains(gotPath, "/locations/") {
		t.Errorf("path = %q, want no project/location segments", gotPath)
	}
}
