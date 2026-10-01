package ollama

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/httpclient"
	"github.com/lycaon/lycaon/internal/llm/discovery"
	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/llm/providerwire"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestOllamaNativeBase(t *testing.T) {
	cases := map[string]string{
		"http://localhost:11434/v1":  "http://localhost:11434",
		"http://localhost:11434/v1/": "http://localhost:11434",
		"http://10.0.20.5:11434/v1":  "http://10.0.20.5:11434",
		"http://localhost:11434":     "http://localhost:11434",
	}
	for in, want := range cases {
		if got := discovery.OllamaNativeBase(in); got != want {
			t.Fatalf("ollamaNativeBase(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSizeNumCtx(t *testing.T) {
	// Small prompts use the smallest safe bucket.
	got, fits := sizeNumCtx(200, 512, 131072)
	if !fits || got != 8192 {
		t.Fatalf("small prompt: got num_ctx=%d fits=%v, want 8192/true", got, fits)
	}
	// Prompts in one bucket reuse the same context size.
	a, _ := sizeNumCtx(12054, 8192, 262144)
	b, _ := sizeNumCtx(14160, 8192, 262144)
	if a != b {
		t.Fatalf("prompt growth crossed buckets and would reload: %d vs %d", a, b)
	}
	if a != 32768 {
		t.Fatalf("12k/14k prompt should bucket to 32768, got %d", a)
	}
	// Prompt that overruns the model ceiling reports !fits and clamps to the max.
	got, fits = sizeNumCtx(9000, 2048, 4096)
	if fits {
		t.Fatalf("overflow prompt should not fit (got num_ctx=%d)", got)
	}
	if got != 4096 {
		t.Fatalf("overflow num_ctx clamped to %d, want 4096", got)
	}
	// A prompt that fits alone still fails if the requested completion and
	// headroom do not fit the physical model context.
	got, fits = sizeNumCtx(2500, 1024, 4096)
	if fits || got != 4096 {
		t.Fatalf("output-crowded context: got num_ctx=%d fits=%v, want 4096/false", got, fits)
	}
	// Unknown ceiling (discovery failed) always fits and sizes from the estimate.
	got, fits = sizeNumCtx(9000, 2048, 0)
	if !fits || got < 9000 {
		t.Fatalf("unknown ceiling: got num_ctx=%d fits=%v", got, fits)
	}
}

func TestOllamaPromptProjectionReservesTokenizerHeadroom(t *testing.T) {
	p := New("desktop", "http://unused", "", []modelinfo.Entry{{
		ID: "gemma", ContextLength: 131072, MaxTokens: 1536,
	}})
	req := modelcall.CompletionRequest{
		Model: "gemma",
		Messages: []api.Message{{Role: api.MessageRoleUser,
			Content: strings.Repeat("a", 24000)}}, // generic estimate: 6000
	}
	built, estimated, err := p.Prepare(t.Context(), req, false)
	testutil.FailErr(t, "build request", err)
	if estimated != 6000 {
		t.Fatalf("generic estimate = %d want 6000", estimated)
	}
	if built.Options.NumCtx != 16384 {
		t.Fatalf("num_ctx = %d want 16384 with tokenizer headroom", built.Options.NumCtx)
	}

	p.observePromptTokens("gemma", 100, 220)
	if projected := p.projectPromptTokens("gemma", 100); projected != 242 {
		t.Fatalf("learned projection = %d want 242", projected)
	}
	clone := p.WithEffectiveModels([]modelinfo.Entry{{ID: "gemma", ContextLength: 131072}})
	if projected := clone.projectPromptTokens("gemma", 100); projected != 242 {
		t.Fatalf("cloned projection = %d want 242", projected)
	}
}

func TestOllamaCompleteSurfacesOutputTruncation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"message":           map[string]any{"role": "assistant", "content": `{"facts":["partial"`},
			"done":              true,
			"done_reason":       "length",
			"prompt_eval_count": 9000,
			"eval_count":        1536,
		})
	}))
	defer srv.Close()

	p := New("desktop", srv.URL+"/v1", "", []modelinfo.Entry{{ID: "gemma", ContextLength: 131072}})
	out, err := p.Complete(t.Context(), modelcall.CompletionRequest{
		Model: "gemma", MaxTokens: 1536,
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "summarize"}},
	})
	if !errors.Is(err, failure.ErrProviderOutputTruncated) {
		t.Fatalf("error = %v want output truncation", err)
	}
	if out == nil || out.Usage.CompletionTokens != 1536 || !strings.Contains(out.Content, "partial") {
		t.Fatalf("partial completion = %+v", out)
	}
	truncated, ok := failure.AsProviderOutputTruncated(err)
	if !ok || truncated.ContextTokens < 8192 || truncated.FinishReason != "length" {
		t.Fatalf("truncation detail = %+v", truncated)
	}
}

func TestOllamaStreamSurfacesOutputTruncationWithUsage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		io.WriteString(w, `{"message":{"role":"assistant","content":"partial"},"done":false}`+"\n")
		io.WriteString(w, `{"message":{"role":"assistant"},"done":true,"done_reason":"length","prompt_eval_count":9000,"eval_count":1536}`+"\n")
	}))
	defer srv.Close()

	p := New("desktop", srv.URL+"/v1", "", []modelinfo.Entry{{ID: "gemma", ContextLength: 131072}})
	out, _, err := modelcall.CollectStream(mustStream(t, p, modelcall.CompletionRequest{
		Model: "gemma", MaxTokens: 1536,
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "summarize"}},
	}))
	if !errors.Is(err, failure.ErrProviderOutputTruncated) {
		t.Fatalf("error = %v want output truncation", err)
	}
	if out.Content != "partial" || out.Usage.PromptTokens != 9000 || out.Usage.CompletionTokens != 1536 {
		t.Fatalf("partial stream = %+v", out)
	}
}

func TestMessagesToOllamaRoundTrip(t *testing.T) {
	msgs := []api.Message{
		{Role: api.MessageRoleSystem, Content: "orchestrate"},
		{Role: api.MessageRoleUser, Content: "build checkers"},
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{
			{Name: "update_progress", Args: map[string]any{"plan": "x"}},
		}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Content: "ok"}},
	}
	out := ProjectMessages(msgs, false, "")
	if len(out) != 4 {
		t.Fatalf("mapped %d messages, want 4", len(out))
	}
	if out[2].Role != "assistant" || len(out[2].ToolCalls) != 1 {
		t.Fatalf("assistant tool call not mapped: %+v", out[2])
	}
	if out[2].ToolCalls[0].Function.Arguments["plan"] != "x" {
		t.Fatalf("tool args not object-shaped: %+v", out[2].ToolCalls[0])
	}
	if out[3].Role != "tool" || out[3].Content != "ok" {
		t.Fatalf("tool result not mapped: %+v", out[3])
	}
}

func TestMessagesToOllamaNilArgsAreObject(t *testing.T) {
	out := ProjectMessages([]api.Message{{
		Role:      api.MessageRoleAssistant,
		ToolCalls: []api.ToolCall{{Name: "write", Args: nil}},
	}}, false, "")
	if len(out) != 1 || len(out[0].ToolCalls) != 1 {
		t.Fatalf("got %+v", out)
	}
	if out[0].ToolCalls[0].Function.Arguments == nil {
		t.Fatal("nil Args encoded as nil")
	}
}

func TestMessagesToOllamaProjectsVisionImage(t *testing.T) {
	png := tinyPNG(t)
	providerwire.SetVisualBytesResolver(func(sessionID, artifactID string) ([]byte, string, bool) {
		return png, "image/png", true
	})
	t.Cleanup(func() { providerwire.SetVisualBytesResolver(nil) })

	out := ProjectMessages([]api.Message{{
		Role: api.MessageRoleUser, Content: "describe", ArtifactIDs: []string{"art-1"},
	}}, true, "session-1")
	if len(out) != 1 || len(out[0].Images) != 1 {
		t.Fatalf("messages = %+v", out)
	}
	decoded, err := base64.StdEncoding.DecodeString(out[0].Images[0])
	testutil.FailErr(t, "decode Ollama image", err)
	if string(decoded) != string(png) {
		t.Fatal("Ollama image bytes differ from normalized artifact")
	}
}

func TestOllamaStreamCompletes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/show":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"model_info": map[string]any{"qwen3.context_length": 131072},
			})
		case "/api/chat":
			var body Request
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body.Options.NumCtx < 8192 {
				t.Errorf("num_ctx not raised above Ollama's 4096 default: %d", body.Options.NumCtx)
			}
			w.Header().Set("Content-Type", "application/x-ndjson")
			io.WriteString(w, `{"message":{"role":"assistant","content":"hello "},"done":false}`+"\n")
			io.WriteString(w, `{"message":{"role":"assistant","content":"world"},"done":false}`+"\n")
			io.WriteString(w, `{"message":{"role":"assistant","content":"","tool_calls":[{"function":{"name":"wait","arguments":{}}}]},"done":true,"prompt_eval_count":120,"eval_count":8}`+"\n")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	p := New("desktop", srv.URL+"/v1", "", []modelinfo.Entry{{ID: "qwen3.5:27b"}})
	ch, err := p.Stream(context.Background(), modelcall.CompletionRequest{
		Model:    "qwen3.5:27b",
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "hi"}},
	})
	if err != nil {
		testutil.FailErr(t, "open stream", err)
	}
	completion, _, err := modelcall.CollectStream(ch)
	if err != nil {
		testutil.FailErr(t, "collect stream", err)
	}
	if completion.Content != "hello world" {
		t.Fatalf("content = %q, want %q", completion.Content, "hello world")
	}
	if len(completion.ToolCalls) != 1 || completion.ToolCalls[0].Name != "wait" {
		t.Fatalf("tool calls = %+v", completion.ToolCalls)
	}
	if completion.Usage.PromptTokens != 120 {
		t.Fatalf("prompt tokens = %d, want 120", completion.Usage.PromptTokens)
	}
}

func TestOllamaStreamAccumulatesParallelToolCalls(t *testing.T) {
	// Parallel task calls may arrive across stream objects.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/show":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"model_info": map[string]any{"qwen3.context_length": 131072},
			})
		case "/api/chat":
			w.Header().Set("Content-Type", "application/x-ndjson")
			io.WriteString(w, `{"message":{"role":"assistant","content":"dispatching"},"done":false}`+"\n")
			io.WriteString(w, `{"message":{"role":"assistant","tool_calls":[{"function":{"name":"task","arguments":{"agent_type":"implementer","brief":{"goal":"board","done_when":["done"]}}}}]},"done":false}`+"\n")
			io.WriteString(w, `{"message":{"role":"assistant","tool_calls":[{"function":{"name":"task","arguments":{"agent_type":"implementer","brief":{"goal":"ai","done_when":["done"]}}}}]},"done":false}`+"\n")
			io.WriteString(w, `{"message":{"role":"assistant","content":""},"done":true,"prompt_eval_count":50,"eval_count":12}`+"\n")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	p := New("desktop", srv.URL+"/v1", "", []modelinfo.Entry{{ID: "qwen3.5:27b"}})
	completion, _, err := modelcall.CollectStream(mustStream(t, p, modelcall.CompletionRequest{
		Model:    "qwen3.5:27b",
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "build it"}},
	}))
	if err != nil {
		testutil.FailErr(t, "collect stream", err)
	}
	if len(completion.ToolCalls) != 2 {
		t.Fatalf("tool calls = %+v, want 2 (both legs survive)", completion.ToolCalls)
	}
	first := completion.ToolCalls[0].Args["brief"].(map[string]any)["goal"]
	second := completion.ToolCalls[1].Args["brief"].(map[string]any)["goal"]
	if first != "board" || second != "ai" {
		t.Fatalf("tool call order/args wrong: %+v", completion.ToolCalls)
	}
	if completion.ToolCalls[0].ID == completion.ToolCalls[1].ID {
		t.Fatalf("parallel calls share an ID %q — host dedupes them to one", completion.ToolCalls[0].ID)
	}
	// Missing provider IDs receive distinct host IDs.
	for i, tc := range completion.ToolCalls {
		if tc.ID == "" || tc.ID == "call_"+string(rune('0'+i)) {
			t.Fatalf("tool call #%d id = %q want host-minted", i, tc.ID)
		}
		if tc.WireID != "" {
			t.Fatalf("tool call #%d carries WireID %q — Ollama has no provider token", i, tc.WireID)
		}
	}
}

func TestOllamaContextTooSmallSurfaces(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/show" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"model_info": map[string]any{"llama.context_length": 4096},
			})
			return
		}
		t.Errorf("unexpected call to %s when prompt cannot fit", r.URL.Path)
	}))
	defer srv.Close()

	p := New("desktop", srv.URL+"/v1", "", []modelinfo.Entry{{ID: "tiny"}})
	bigPrompt := strings.Repeat("token ", 40000) // ~60k tokens, far over a 4096 ceiling
	_, err := p.Stream(context.Background(), modelcall.CompletionRequest{
		Model:    "tiny",
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: bigPrompt}},
	})
	e, ok := failure.AsProviderContextTooSmall(err)
	if !ok {
		testutil.FailErr(t, "expected context-too-small error", err)
	}
	if e.MaxContext != 4096 || e.Model != "tiny" {
		t.Fatalf("error fields = %+v", e)
	}
}

func TestOllamaContextLengthOverrideSkipsProbe(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/show" {
			t.Errorf("/api/show probed despite configured context_length")
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	p := New("desktop", srv.URL+"/v1", "", []modelinfo.Entry{{ID: "pinned", ContextLength: 8192}})
	if got := p.ContextLimit(context.Background(), "pinned"); got != 8192 {
		t.Fatalf("maxContextForModel = %d, want 8192 (override)", got)
	}
}

func TestOllamaThinkControl(t *testing.T) {
	withThinkingRules(t, bundledThinkingRules(t))
	var lastThink any
	newProvider := func(models []modelinfo.Entry) *Provider {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/show" {
				_ = json.NewEncoder(w).Encode(map[string]any{"model_info": map[string]any{"qwen3.context_length": 131072}})
				return
			}
			var body Request
			_ = json.NewDecoder(r.Body).Decode(&body)
			lastThink = body.Think
			w.Header().Set("Content-Type", "application/x-ndjson")
			io.WriteString(w, `{"message":{"role":"assistant","content":"ok"},"done":true,"prompt_eval_count":10,"eval_count":1}`+"\n")
		}))
		t.Cleanup(srv.Close)
		return New("desktop", srv.URL+"/v1", "", models)
	}
	orchReq := func(model string) modelcall.CompletionRequest {
		return modelcall.CompletionRequest{
			Model:    model,
			Messages: []api.Message{{Role: api.MessageRoleUser, Content: "go"}},
			Tools:    []tools.ToolMeta{{Name: "task", ArgsSchema: map[string]any{"type": "object"}}},
		}
	}

	// Boolean controls enable reasoning for normal agent turns.
	p := newProvider([]modelinfo.Entry{{ID: "qwen3.5:27b"}})
	_, _, _ = modelcall.CollectStream(mustStream(t, p, orchReq("qwen3.5:27b")))
	if b, ok := lastThink.(bool); !ok || !b {
		t.Fatalf("orchestration default: think = %#v, want bool true", lastThink)
	}

	// Configured reasoning_effort => leveled thinking string, even on orchestration.
	p = newProvider([]modelinfo.Entry{{ID: "gpt-oss:20b", ReasoningEffort: "low"}})
	_, _, _ = modelcall.CollectStream(mustStream(t, p, orchReq("gpt-oss:20b")))
	if s, ok := lastThink.(string); !ok || s != "low" {
		t.Fatalf("configured effort: think = %#v, want string \"low\"", lastThink)
	}

	minimal := orchReq("gpt-oss:20b")
	minimal.Think = modelcall.ThinkOff
	_, _, _ = modelcall.CollectStream(mustStream(t, p, minimal))
	if lastThink != "low" {
		t.Fatalf("level-only recovery: think = %#v, want low", lastThink)
	}

	// Prose turns use the same reasoning policy.
	p = newProvider([]modelinfo.Entry{{ID: "qwen3.5:27b"}})
	_, _, _ = modelcall.CollectStream(mustStream(t, p, modelcall.CompletionRequest{
		Model:    "qwen3.5:27b",
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "summarize"}},
	}))
	if lastThink != true {
		t.Fatalf("open turn: think = %#v, want true", lastThink)
	}
}

func TestOllamaReasoningCapturedNotInContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/show" {
			_ = json.NewEncoder(w).Encode(map[string]any{"model_info": map[string]any{"qwen3.context_length": 131072}})
			return
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		io.WriteString(w, `{"message":{"role":"assistant","thinking":"let me weigh "},"done":false}`+"\n")
		io.WriteString(w, `{"message":{"role":"assistant","thinking":"the options"},"done":false}`+"\n")
		io.WriteString(w, `{"message":{"role":"assistant","content":"the answer"},"done":true,"prompt_eval_count":20,"eval_count":5}`+"\n")
	}))
	defer srv.Close()
	p := New("desktop", srv.URL+"/v1", "", []modelinfo.Entry{{ID: "qwen3.5:27b"}})
	completion, _, err := modelcall.CollectStream(mustStream(t, p, modelcall.CompletionRequest{
		Model:    "qwen3.5:27b",
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "decide"}},
	}))
	if err != nil {
		testutil.FailErr(t, "collect", err)
	}
	if completion.Content != "the answer" {
		t.Fatalf("content = %q, reasoning leaked in?", completion.Content)
	}
	if completion.Reasoning != "let me weigh the options" {
		t.Fatalf("reasoning = %q, want accumulated thinking", completion.Reasoning)
	}
}

func TestDecodeOllamaStreamRejectsMissingDone(t *testing.T) {
	ch := decodeOllamaStream(t.Context(), strings.NewReader(
		`{"message":{"content":"partial"},"done":false}`+"\n",
	), "desktop", "model", 8192, nil)
	if _, _, err := modelcall.CollectStream(ch); err == nil || !strings.Contains(err.Error(), "before done") {
		t.Fatalf("error = %v, want missing done", err)
	}
}

func mustStream(t *testing.T, p *Provider, req modelcall.CompletionRequest) <-chan modelcall.StreamChunk {
	t.Helper()
	ch, err := p.Stream(context.Background(), req)
	if err != nil {
		testutil.FailErr(t, "open stream", err)
	}
	return ch
}

func TestEstimatePromptTokensCountsTools(t *testing.T) {
	req := modelcall.CompletionRequest{
		Messages: []api.Message{{Role: api.MessageRoleSystem, Content: strings.Repeat("a", 400)}},
		Tools: []tools.ToolMeta{
			{Name: "update_progress", Description: "plan", ArgsSchema: map[string]any{"type": "object"}},
		},
	}
	got := estimatePromptTokens(req)
	if got < 100 {
		t.Fatalf("estimate too low: %d", got)
	}
}

// Non-streaming generation needs a longer header wait.
func TestOllamaCompleteUsesLongerHeaderTimeout(t *testing.T) {
	p := New("desktop", "http://localhost:11434/v1", "", []modelinfo.Entry{{ID: "m"}})
	streamTO := responseHeaderTimeoutOf(p.streamClient)
	completeTO := responseHeaderTimeoutOf(p.completeClient)
	if streamTO != providerprofile.LocalInferenceStreamResponseHeaderTimeout {
		t.Fatalf("stream ResponseHeaderTimeout = %v, want %v", streamTO, providerprofile.LocalInferenceStreamResponseHeaderTimeout)
	}
	if completeTO != providerprofile.LocalInferenceCompleteResponseHeaderTimeout {
		t.Fatalf("complete ResponseHeaderTimeout = %v, want %v", completeTO, providerprofile.LocalInferenceCompleteResponseHeaderTimeout)
	}
	if completeTO <= streamTO {
		t.Fatalf("complete timeout %v must exceed stream TTFT %v", completeTO, streamTO)
	}
	if streamTO <= httpclient.DefaultResponseHeader {
		t.Fatalf("local stream TTFT %v must exceed remote TTFT %v", streamTO, httpclient.DefaultResponseHeader)
	}
}
