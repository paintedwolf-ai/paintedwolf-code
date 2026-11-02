package llm

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/llm/providers/openaicompat"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestCompatibleTransportPreservesReasoningChannels(t *testing.T) {
	withThinkingRules(t, []modelinfo.ThinkingRule{{Match: []string{"fixture"}, Style: "none", ReasoningMarkers: &thinkMarkers}})
	for _, tc := range []struct {
		name, content, fields, visible, reasoning string
	}{
		{"coincident reasoning", "<think>\nAnswer", `"reasoning":"private"`, "Answer", "private"},
		{"coincident reasoning content", "<think>\nAnswer", `"reasoning_content":"private"`, "Answer", "private"},
		{"coincident reasoning details", "<think>\nAnswer", `"reasoning_details":[{"type":"reasoning.text","text":"private","index":0}]`, "Answer", "private"},
		{"inline block", "<think>private</think>Answer", `"reasoning_content":""`, "Answer", "private"},
		{"literal closing marker", "Quote </think> literally", `"reasoning_content":""`, "Quote </think> literally", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			text, err := json.Marshal(tc.content)
			testutil.FailErr(t, "encode fixture text", err)
			message := fmt.Sprintf(`{"content":%s,%s}`, text, tc.fields)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct { Stream bool `json:"stream"` }
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Errorf("decode request: %v", err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				if request.Stream {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":%s,\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", message)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprintf(w, `{"choices":[{"message":%s,"finish_reason":"stop"}]}`, message)
			}))
			defer server.Close()
			driver := openaicompat.New("fixture", server.URL+"/v1", "", []modelinfo.Entry{{ID: "fixture-model"}}).WithProfile(providerprofile.OpenAI())
			snapshot := newProviderRegistrySnapshot(providerRegistrySnapshotInput{Providers: map[string]modelcall.Provider{"fixture": driver}})
			registry := newTestRegistry()
			registry.snapshot.Store(snapshot)
			provider := registry.decorateProvider(snapshot, driver)
			request := modelcall.CompletionRequest{Model: "fixture-model", Messages: []api.Message{{Role: api.MessageRoleUser, Content: "Answer."}}}
			completed, err := provider.Complete(t.Context(), request)
			testutil.FailErr(t, "complete fixture", err)
			stream, err := provider.Stream(t.Context(), request)
			testutil.FailErr(t, "stream fixture", err)
			streamed, _, err := modelcall.CollectStream(stream)
			testutil.FailErr(t, "collect fixture", err)
			for name, got := range map[string]*modelcall.Completion{"complete": completed, "stream": streamed} {
				if got.Content != tc.visible || got.Reasoning != tc.reasoning {
					t.Fatalf("%s content=%q reasoning=%q, want content=%q reasoning=%q", name, got.Content, got.Reasoning, tc.visible, tc.reasoning)
				}
			}
		})
	}
}
