package promptloop

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providers/openaicompat"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestPromptLoopOpenAIProviderTwoTurns(t *testing.T) {
	var reqNum atomic.Int32
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := reqNum.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("expected flusher")
		}
		if n == 1 {
			var body struct {
				Messages []struct {
					ToolCallID string `json:"tool_call_id"`
				} `json:"messages"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			chunks := []map[string]any{
				{"choices": []map[string]any{{"delta": map[string]any{
					"tool_calls": []map[string]any{{
						"index": 0,
						"id":    "call_1",
						"type":  "function",
						"function": map[string]any{
							"name":      "read",
							"arguments": `{"path":"x"}`,
						},
					}},
				}}}},
				{"choices": []map[string]any{{"finish_reason": "tool_calls"}}},
			}
			for _, chunk := range chunks {
				data, _ := json.Marshal(chunk)
				fmt.Fprintf(w, "data: %s\n\n", data)
				flusher.Flush()
			}
		} else {
			var body struct {
				Messages []struct {
					Role      string `json:"role"`
					ToolCalls []struct {
						ID string `json:"id"`
					} `json:"tool_calls"`
					ToolCallID string `json:"tool_call_id"`
				} `json:"messages"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode: %v", err)
			}
			// Tool results pair with the host-minted assistant call ID.
			var assistantCallID string
			foundToolResult := false
			for _, m := range body.Messages {
				if m.Role == "assistant" && len(m.ToolCalls) > 0 {
					assistantCallID = m.ToolCalls[0].ID
				}
				if m.Role == "tool" && m.ToolCallID != "" && m.ToolCallID == assistantCallID {
					foundToolResult = true
				}
			}
			if assistantCallID == "" {
				t.Error("second request missing assistant tool_call id")
			}
			if !foundToolResult {
				t.Error("second request missing tool message paired to its assistant call id")
			}
			contentChunk, _ := json.Marshal(map[string]any{
				"choices": []map[string]any{{"delta": map[string]any{"content": "done"}, "finish_reason": "stop"}},
				"usage":   map[string]any{"prompt_tokens": 5, "completion_tokens": 2},
			})
			fmt.Fprintf(w, "data: %s\n\n", contentChunk)
			flusher.Flush()
		}
		fmt.Fprintln(w, "data: [DONE]")
		flusher.Flush()
	}))
	defer mockServer.Close()

	reg := tools.NewStubRegistry()
	if err := reg.Register("read", func(_ context.Context, _ map[string]any, _ tools.ToolContext) (string, error) {
		return "file-body", nil
	}); err != nil {
		t.Fatal(err)
	}

	provider := openaicompat.New("openai-test", mockServer.URL, "test-key", []modelinfo.Entry{{ID: "gpt-4o"}})
	loop := NewPromptLoopForTest(PromptLoopDeps{
		Context: ContextDeps{
			Limits: func(context.Context, *api.Session) settings.SessionLimits { return settings.DefaultSessionLimits() },
			Tools:  reg,
			BuildMessages: func(_ context.Context, _ *api.Session, history []api.Message, _ *inject.CoordinatorTurnFrame) ([]api.Message, error) {
				return history, nil
			},
		},
		Model: ModelDeps{
			LLM: provider,
		},
		Projection: ProjectionDeps{
			AppendMessages: func(_ context.Context, _ string, msgs ...api.Message) error {
				return nil
			},
		},
	})
	sess := &api.Session{ID: "s1", Posture: api.SessionPostureBuild, WorkspacePath: t.TempDir()}
	result, err := loop.Run(context.Background(), PromptRunInput{
		SessionID: "s1",
		Session:   sess,
		History:   []api.Message{{Role: api.MessageRoleUser, Content: "read"}},
		ProfileID: "coordinator",
		ToolCtx: tools.ToolContext{
			Identity: tools.InvocationIdentity{SessionID: "s1"},
		},
	})
	testutil.FailErr(t, "loop.Run failed", err)
	if result.LastAssistantContent != "done" {
		t.Fatalf("content = %q", result.LastAssistantContent)
	}
	if reqNum.Load() < 2 {
		t.Fatalf("requests = %d, want >= 2", reqNum.Load())
	}
}

// TestPromptLoopReassignsCollidingToolCallIDsAcrossTurns keeps host IDs unique.
func TestPromptLoopReassignsCollidingToolCallIDsAcrossTurns(t *testing.T) {
	var reqNum atomic.Int32
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := reqNum.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("expected flusher")
		}
		emitToolCall := func(path string) {
			chunks := []map[string]any{
				{"choices": []map[string]any{{"delta": map[string]any{
					"tool_calls": []map[string]any{{
						"index": 0,
						"id":    "call_0", // provider restarts the counter every turn
						"type":  "function",
						"function": map[string]any{
							"name":      "read",
							"arguments": fmt.Sprintf(`{"path":%q}`, path),
						},
					}},
				}}}},
				{"choices": []map[string]any{{"finish_reason": "tool_calls"}}},
			}
			for _, chunk := range chunks {
				data, _ := json.Marshal(chunk)
				fmt.Fprintf(w, "data: %s\n\n", data)
				flusher.Flush()
			}
		}
		switch n {
		case 1:
			emitToolCall("a.go")
		case 2:
			emitToolCall("b.go")
		default:
			contentChunk, _ := json.Marshal(map[string]any{
				"choices": []map[string]any{{"delta": map[string]any{"content": "done"}, "finish_reason": "stop"}},
				"usage":   map[string]any{"prompt_tokens": 5, "completion_tokens": 2},
			})
			fmt.Fprintf(w, "data: %s\n\n", contentChunk)
			flusher.Flush()
		}
		fmt.Fprintln(w, "data: [DONE]")
		flusher.Flush()
	}))
	defer mockServer.Close()

	reg := tools.NewStubRegistry()
	if err := reg.Register("read", func(_ context.Context, args map[string]any, _ tools.ToolContext) (string, error) {
		return "body-of-" + fmt.Sprint(args["path"]), nil
	}); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var appended []api.Message
	provider := openaicompat.New("openai-test", mockServer.URL, "test-key", []modelinfo.Entry{{ID: "gpt-4o"}})
	loop := NewPromptLoopForTest(PromptLoopDeps{
		Context: ContextDeps{
			Limits: func(context.Context, *api.Session) settings.SessionLimits { return settings.DefaultSessionLimits() },
			Tools:  reg,
			BuildMessages: func(_ context.Context, _ *api.Session, history []api.Message, _ *inject.CoordinatorTurnFrame) ([]api.Message, error) {
				return history, nil
			},
		},
		Model: ModelDeps{
			LLM: provider,
		},
		Projection: ProjectionDeps{
			AppendMessages: func(_ context.Context, _ string, msgs ...api.Message) error {
				mu.Lock()
				appended = append(appended, msgs...)
				mu.Unlock()
				return nil
			},
		},
	})
	sess := &api.Session{ID: "s1", Posture: api.SessionPostureBuild, WorkspacePath: t.TempDir()}
	_, err := loop.Run(context.Background(), PromptRunInput{
		SessionID: "s1",
		Session:   sess,
		History:   []api.Message{{Role: api.MessageRoleUser, Content: "read both"}},
		ProfileID: "coordinator",
		ToolCtx: tools.ToolContext{
			Identity: tools.InvocationIdentity{SessionID: "s1"},
		},
	})
	testutil.FailErr(t, "loop.Run failed", err)

	mu.Lock()
	defer mu.Unlock()
	var toolResultIDs []string
	for _, m := range appended {
		if m.Role == api.MessageRoleTool && m.ToolResult != nil {
			toolResultIDs = append(toolResultIDs, m.ToolResult.ToolCallID)
		}
	}
	if len(toolResultIDs) != 2 {
		t.Fatalf("tool result count = %d want 2 (ids=%v)", len(toolResultIDs), toolResultIDs)
	}
	if toolResultIDs[0] == toolResultIDs[1] {
		t.Fatalf("checkpoint anchors collide across turns: both %q", toolResultIDs[0])
	}
	for _, id := range toolResultIDs {
		if id == "" {
			t.Fatal("tool result missing tool_call_id anchor")
		}
	}
}
