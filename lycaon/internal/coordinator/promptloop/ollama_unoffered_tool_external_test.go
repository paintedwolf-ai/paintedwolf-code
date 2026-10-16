package promptloop_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/promptloop"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providers/ollama"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

type recordingLLM struct {
	modelcall.LLMClient
	requests []modelcall.CompletionRequest
}

func (r *recordingLLM) Stream(ctx context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	r.requests = append(r.requests, req)
	return r.LLMClient.Stream(ctx, req)
}

// Ollama's parser rejection of an unoffered name reaches the model as the
// host's TOOL_NOT_OFFERED refusal and never runs the named tool.
func TestLoopRefusesOllamaUnofferedToolRejection(t *testing.T) {
	for _, beforeOutput := range []bool{false, true} {
		t.Run(map[bool]string{false: "after output", true: "before output"}[beforeOutput], func(t *testing.T) {
			var turns atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/chat" {
					http.NotFound(w, r)
					return
				}
				_, _ = io.Copy(io.Discard, r.Body)
				if turns.Add(1) == 1 {
					if beforeOutput {
						w.WriteHeader(http.StatusInternalServerError)
					} else {
						_, _ = io.WriteString(w, `{"message":{"content":"Editing the file."},"done":false}`+"\n")
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"error": "tool 'edit' not found"})
					return
				}
				_, _ = io.WriteString(w, `{"message":{"content":"done"},"done":true,"done_reason":"stop","prompt_eval_count":5,"eval_count":1}`+"\n")
			}))
			defer srv.Close()

			editCalls := 0
			reg := tools.NewStubRegistry()
			_ = reg.Register("read", func(context.Context, map[string]any, tools.ToolContext) (string, error) { return "ok", nil })
			_ = reg.Register("edit", func(context.Context, map[string]any, tools.ToolContext) (string, error) {
				editCalls++
				return "edited", nil
			})
			mem := store.NewMemory()
			ctx := context.Background()
			sess, err := mem.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
			testutil.FailErr(t, "create session", err)
			sess.ParentSessionID = "parent-1"
			sess.AgentType = orchestration.ProfileImplementer
			catalog, err := llm.LoadProviderConfig()
			testutil.FailErr(t, "load provider catalog", err)
			client := &recordingLLM{LLMClient: ollama.New("desktop", srv.URL+"/v1", "", []modelinfo.Entry{{ID: "ministral-3:3b", ContextLength: 32768}}).WithToolCallRejections(catalog.ToolCallRejectionProfiles["ollama"])}
			deps := promptloop.StoreDeps(mem)
			deps.Model.LLM = client
			deps.Context.Policy = &fixedToolPolicy{metas: []tools.ToolMeta{{Name: "read"}}}
			deps.Context.Tools = reg
			result, err := promptloop.NewPromptLoopForTest(deps).Run(ctx, promptloop.PromptRunInput{
				SessionID: sess.ID,
				Session:   sess,
				History:   userHistory("give me the gist"),
				ProfileID: "implementer",
				ToolCtx:   tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: sess.ID, Agent: "implementer"}},
			})
			testutil.FailErr(t, "loop.Run failed", err)
			if editCalls != 0 {
				t.Fatalf("unoffered edit ran %d times", editCalls)
			}
			if result.LastAssistantContent != "done" || len(client.requests) != 2 {
				t.Fatalf("content = %q after %d requests, want recovery on the next request", result.LastAssistantContent, len(client.requests))
			}
			if slices.ContainsFunc(client.requests[0].Tools, func(m tools.ToolMeta) bool { return m.Name == "edit" }) {
				t.Fatal("fixture offered edit")
			}
			var editCallID string
			refused := false
			for _, msg := range client.requests[1].Messages {
				for _, call := range msg.ToolCalls {
					if call.Name == "edit" {
						editCallID = call.ID
					}
				}
				if msg.ToolResult != nil && editCallID != "" && msg.ToolResult.ToolCallID == editCallID &&
					msg.ToolResult.Outcome == api.ToolResultOutcomeRejected && slices.Contains(msg.ToolResult.Codes, "TOOL_NOT_OFFERED") {
					refused = true
				}
			}
			if !refused {
				t.Fatal("next model request lacks the TOOL_NOT_OFFERED refusal for the edit call")
			}
		})
	}
}
