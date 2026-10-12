package llm

import (
	"encoding/json"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/llm/providers/anthropic"
	"github.com/lycaon/lycaon/internal/llm/providers/bedrock"
	"github.com/lycaon/lycaon/internal/llm/providers/ollama"
	"github.com/lycaon/lycaon/internal/llm/providers/openaicompat"
	"github.com/lycaon/lycaon/internal/llm/providers/vertexexpress"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestThinkingEvidenceControlsEveryAdapterWire(t *testing.T) {
	withThinkingRules(t, bundledThinkingRules(t))
	profiles := map[string]providerprofile.Profile{
		"openai": providerprofile.OpenAIVendor(), "openai-compatible": providerprofile.OpenAI(),
		"azure": providerprofile.Azure(), "vertex": providerprofile.Vertex(),
		"fireworks": providerprofile.Fireworks(), "together": providerprofile.Together(),
		"gemini": providerprofile.Gemini(), "openrouter": providerprofile.Openrouter(),
		"lmstudio": providerprofile.Lmstudio(), "omlx": providerprofile.Omlx(),
		"litellm-proxy": providerprofile.Litellm(), "cloudflare-workers-ai": providerprofile.CloudflareWorkersAI(),
	}
	for _, state := range []string{"unsupported", "unknown"} {
		for name, profile := range profiles {
			t.Run(name+"/"+state, func(t *testing.T) {
				entry := modelinfo.Entry{ID: "qwen3", DiscoveredThinking: &modelinfo.ThinkingCapabilities{State: state}}
				p := openaicompat.New(name, "http://unused.invalid", "", nil).WithProfile(profile)
				p = hydrateProviderModels(p, []modelinfo.Entry{entry}).(*openaicompat.Provider)
				for _, stream := range []bool{false, true} {
					body, err := p.Prepare(nonReasoningRequest(entry.ID), stream)
					testutil.FailErr(t, "prepare compatible request", err)
					assertNoThinkingFields(t, body)
				}
			})
		}
		for _, name := range []string{"ollama", "anthropic", "vertex-express"} {
			t.Run(name+"/"+state, func(t *testing.T) {
				entry := modelinfo.Entry{ID: "qwen3", ContextLength: 65536, DiscoveredThinking: &modelinfo.ThinkingCapabilities{State: state}}
				for _, stream := range []bool{false, true} {
					req := nonReasoningRequest(entry.ID)
					var body any
					switch name {
					case "ollama":
						p := hydrateProviderModels(ollama.New(name, "http://unused.invalid", "", nil), []modelinfo.Entry{entry}).(*ollama.Provider)
						prepared, _, err := p.Prepare(t.Context(), req, stream)
						testutil.FailErr(t, "prepare native request", err)
						body = prepared
					case "anthropic":
						p := hydrateProviderModels(anthropic.New(name, "http://unused.invalid", "", nil), []modelinfo.Entry{entry}).(*anthropic.Provider)
						body = p.Prepare(req, stream)
					case "vertex-express":
						p := hydrateProviderModels(vertexexpress.New(name, "http://unused.invalid", "", nil), []modelinfo.Entry{entry}).(*vertexexpress.Provider)
						body = p.Prepare(req)
					}
					encoded, err := json.Marshal(body)
					testutil.FailErr(t, "encode native request", err)
					assertNoThinkingFields(t, encoded)
					if req.AttemptBudget.CanReduceReasoning {
						t.Fatal("non-reasoner offered reasoning recovery")
					}
				}
			})
		}
	}
	p := bedrock.New("bedrock", "us-east-1", "", nil)
	if got := modelcall.ResolveThinkingCapabilities(p.Profile(), modelinfo.Entry{}, "model"); got.State != "unsupported" {
		t.Fatalf("Bedrock advertised unsupported controls: %+v", got)
	}
}

func nonReasoningRequest(model string) modelcall.CompletionRequest {
	return modelcall.CompletionRequest{Model: model, Think: modelcall.ThinkHigh,
		Messages:      []api.Message{{Role: api.MessageRoleUser, Content: "hello"}},
		AttemptBudget: &modelcall.CompletionBudget{},
	}
}

func assertNoThinkingFields(t *testing.T, body []byte) {
	t.Helper()
	var fields map[string]any
	testutil.FailErr(t, "decode request fields", json.Unmarshal(body, &fields))
	for _, name := range []string{"think", "thinking", "reasoning", "reasoning_effort", "output_config"} {
		if _, present := fields[name]; present {
			t.Errorf("unsupported control %s in %s", name, body)
		}
	}
	if config, ok := fields["generationConfig"].(map[string]any); ok {
		if _, present := config["thinkingConfig"]; present {
			t.Errorf("unsupported thinkingConfig in %s", body)
		}
	}
}
