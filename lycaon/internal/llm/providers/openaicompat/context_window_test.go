package openaicompat

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestLlamaCppPropsContextDiscovery(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/props" {
			_, _ = w.Write([]byte(`{"default_generation_settings":{"n_ctx":32768}}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	p := New("openai-compatible", srv.URL+"/v1", "", []modelinfo.Entry{{ID: "qwen2.5-coder"}}).
		WithProfile(providerprofile.LocalInference(providerprofile.OpenAI()))

	limit := p.ContextLimit(context.Background(), "qwen2.5-coder")
	if limit != 32768 {
		t.Fatalf("ContextLimit = %d want 32768", limit)
	}
	entry, ok := p.ModelEntry("qwen2.5-coder")
	if !ok || entry.ContextLength != 32768 || entry.ContextLengthSource != "llamacpp-props" {
		t.Fatalf("ModelEntry = %+v, ok=%v; want 32768 from llamacpp-props", entry, ok)
	}
	budget := p.RequestBudget(context.Background(), modelcall.CompletionRequest{Model: "qwen2.5-coder"})
	if budget.Window != 32768 {
		t.Fatalf("RequestBudget.Window = %d want 32768", budget.Window)
	}
}

func TestContextOverflow400ClassificationAndWindowRecording(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		wantWindow int
		wantPrompt int
		wantSource string
	}{
		{
			name:       "llama.cpp exceed_context_size_error with structured n_ctx",
			body:       `{"error":{"code":400,"message":"request (33442 tokens) exceeds the available context size (32768 tokens), try increasing it","type":"exceed_context_size_error","n_prompt_tokens":33442,"n_ctx":32768}}`,
			wantWindow: 32768,
			wantPrompt: 33442,
			wantSource: "exceed_context_size_error",
		},
		{
			name:       "OpenAI-compatible context_length_exceeded code",
			body:       `{"error":{"message":"This model's maximum context length is 32768 tokens. However, your messages resulted in 35000 tokens.","type":"invalid_request_error","param":"messages","code":"context_length_exceeded"}}`,
			wantWindow: 32768,
			wantPrompt: 35000,
			wantSource: "context_length_exceeded",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1/chat/completions" {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusBadRequest)
					_, _ = w.Write([]byte(tc.body))
					return
				}
				w.WriteHeader(http.StatusNotFound)
			}))
			defer srv.Close()

			p := New("openai-compatible", srv.URL+"/v1", "", []modelinfo.Entry{{ID: "local-model"}}).
				WithProfile(providerprofile.LocalInference(providerprofile.OpenAI()))

			_, err := p.Complete(context.Background(), modelcall.CompletionRequest{
				Model:    "local-model",
				Messages: []api.Message{{Role: api.MessageRoleUser, Content: "hello"}},
			})
			if err == nil {
				t.Fatal("expected error from 400 context overflow")
			}
			tooSmall, ok := failure.AsProviderContextTooSmall(err)
			if !ok || tooSmall == nil {
				t.Fatalf("expected ProviderContextTooSmallError, got %T: %v", err, err)
			}
			if tooSmall.MaxContext != tc.wantWindow {
				t.Fatalf("MaxContext = %d want %d", tooSmall.MaxContext, tc.wantWindow)
			}
			if tooSmall.PromptTokens != tc.wantPrompt {
				t.Fatalf("PromptTokens = %d want %d", tooSmall.PromptTokens, tc.wantPrompt)
			}
			if tooSmall.Source != tc.wantSource {
				t.Fatalf("Source = %q want %q", tooSmall.Source, tc.wantSource)
			}
			entry, ok := p.ModelEntry("local-model")
			if !ok || entry.ContextLength != tc.wantWindow || entry.ContextLengthSource != tc.wantSource {
				t.Fatalf("recorded ModelEntry = %+v, ok=%v; want ContextLength=%d Source=%q", entry, ok, tc.wantWindow, tc.wantSource)
			}
		})
	}
}
