package openaicompat

import (
	"github.com/lycaon/lycaon/internal/toolcontract"

	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// streamReasoningServer captures requests while serving fixed SSE frames.
func streamReasoningServer(t *testing.T, frames []string, sent *map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request: %v", err)
			return
		}
		var decoded map[string]any
		if err := json.Unmarshal(body, &decoded); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		*sent = decoded
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		for _, frame := range frames {
			fmt.Fprintf(w, "data: %s\n\n", frame)
			if flusher != nil {
				flusher.Flush()
			}
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		if flusher != nil {
			flusher.Flush()
		}
	}))
}

// Streamed reasoning and tool calls survive the next request.
func TestOpenRouterStreamKeepsReasoningAndReplaysIt(t *testing.T) {
	withThinkingRules(t, nil)
	var sent map[string]any
	srv := streamReasoningServer(t, []string{
		`{"choices":[{"delta":{"reasoning":"the user said yes, ","reasoning_details":[{"type":"reasoning.text","text":"the user said yes, ","index":0,"format":"moonshot-v1"}]}}]}`,
		`{"choices":[{"delta":{"reasoning":"so write the file","reasoning_details":[{"type":"reasoning.text","text":"so write the file","index":0,"signature":"sig-1"}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_9","type":"function","function":{"name":"write","arguments":"{\"path\":\"CONTRIBUTING.md\"}"}}]}}],"usage":{"prompt_tokens":10,"completion_tokens":30}}`,
	}, &sent)
	defer srv.Close()

	p := New("openrouter-1", srv.URL, "key",
		[]modelinfo.Entry{{ID: openrouterKimi}}).WithProfile(providerprofile.Openrouter()).WithReasoningWire(providerprofile.ReasoningWireDetails)

	req := modelcall.CompletionRequest{
		Model:    openrouterKimi,
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "Yes update it."}},
		Tools:    []tools.ToolMeta{{Name: "write"}},
		Debug:    modelcall.RequestDebug{Surface: toolcontract.SurfaceImplementInvestigate, SessionID: "s1"},
	}
	ch, err := p.Stream(t.Context(), req)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	completion, _, err := modelcall.CollectStream(ch)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}

	if len(completion.ToolCalls) != 1 || completion.ToolCalls[0].Name != "write" {
		t.Fatalf("tool calls = %+v want the write call", completion.ToolCalls)
	}
	if completion.Reasoning != "the user said yes, so write the file" {
		t.Fatalf("reasoning = %q", completion.Reasoning)
	}
	if len(completion.ReasoningDetails) != 1 {
		t.Fatalf("reasoning_details = %v want one merged block", completion.ReasoningDetails)
	}
	var block reasoningDetailDelta
	if err := json.Unmarshal(completion.ReasoningDetails[0], &block); err != nil {
		t.Fatalf("unmarshal block: %v", err)
	}
	if block.Text != "the user said yes, so write the file" || block.Signature != "sig-1" || block.Format != "moonshot-v1" {
		t.Fatalf("merged block = %+v", block)
	}

	// Replay the recorded completion.
	completion.ProviderID = "openrouter-1"
	completion.Model = openrouterKimi
	assistant := api.Message{
		Role:           api.MessageRoleAssistant,
		ToolCalls:      completion.ToolCalls,
		ModelReasoning: completion.ModelReasoning(),
	}
	echoed := assistantWire(t, p, []api.Message{assistant})
	if echoed.Reasoning != completion.Reasoning {
		t.Fatalf("replayed reasoning = %q want %q", echoed.Reasoning, completion.Reasoning)
	}
	if len(echoed.ReasoningDetails) != 1 || string(echoed.ReasoningDetails[0]) != string(completion.ReasoningDetails[0]) {
		t.Fatalf("replayed blocks were rewritten: %s", echoed.ReasoningDetails)
	}
}

// Structured reasoning alone keeps the stream active.
func TestOpenRouterStreamAcceptsDetailsWithoutFlatReasoning(t *testing.T) {
	withThinkingRules(t, nil)
	var sent map[string]any
	srv := streamReasoningServer(t, []string{
		`{"choices":[{"delta":{"reasoning_details":[{"type":"reasoning.text","text":"quiet thinking","index":0}]}}]}`,
		`{"choices":[{"delta":{"content":"done"}}],"usage":{"prompt_tokens":5,"completion_tokens":5}}`,
	}, &sent)
	defer srv.Close()

	p := New("openrouter-1", srv.URL, "key",
		[]modelinfo.Entry{{ID: openrouterKimi}}).WithProfile(providerprofile.Openrouter()).WithReasoningWire(providerprofile.ReasoningWireDetails)
	ch, err := p.Stream(t.Context(), modelcall.CompletionRequest{
		Model:    openrouterKimi,
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "go"}},
	})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	completion, _, err := modelcall.CollectStream(ch)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if completion.Reasoning != "quiet thinking" {
		t.Fatalf("reasoning = %q want the block text mirrored", completion.Reasoning)
	}
	if completion.Content != "done" {
		t.Fatalf("content = %q", completion.Content)
	}
	if len(completion.ReasoningDetails) != 1 {
		t.Fatalf("reasoning_details = %v", completion.ReasoningDetails)
	}
}

// Streaming requests retain the resolved transport controls.
func TestOpenRouterStreamSendsGatewayControlsOnTheWire(t *testing.T) {
	withThinkingRules(t, []modelinfo.ThinkingRule{
		{Match: []string{"kimi-k2.7-code"}, Style: string(modelinfo.ThinkStyleThinkingType)},
	})
	var sent map[string]any
	srv := streamReasoningServer(t, []string{
		`{"choices":[{"delta":{"content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`,
	}, &sent)
	defer srv.Close()

	p := New("openrouter-1", srv.URL, "key",
		[]modelinfo.Entry{{ID: openrouterKimi}}).WithProfile(providerprofile.Openrouter()).WithReasoningWire(providerprofile.ReasoningWireDetails)
	ch, err := p.Stream(t.Context(), modelcall.CompletionRequest{
		Model:    openrouterKimi,
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "go"}},
		Tools:    []tools.ToolMeta{{Name: "write"}},
		Debug:    modelcall.RequestDebug{Surface: toolcontract.SurfaceImplementInvestigate},
	})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if _, _, err := modelcall.CollectStream(ch); err != nil {
		t.Fatalf("collect: %v", err)
	}

	raw, err := json.Marshal(sent)
	if err != nil {
		t.Fatalf("marshal captured request: %v", err)
	}
	body := string(raw)
	for _, absent := range []string{`"thinking"`, `"reasoning_effort"`} {
		if strings.Contains(body, absent) {
			t.Fatalf("%s reached OpenRouter: %s", absent, body)
		}
	}
	if !strings.Contains(body, `"require_parameters":true`) {
		t.Fatalf("provider routing missing: %s", body)
	}
	reasoning, ok := sent["reasoning"].(map[string]any)
	if !ok || reasoning["effort"] != "medium" {
		t.Fatalf("reasoning = %v want effort:medium: %s", sent["reasoning"], body)
	}
}
