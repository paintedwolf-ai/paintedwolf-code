package llm

import (
	"errors"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	ollamaprovider "github.com/lycaon/lycaon/internal/llm/providers/ollama"
	"github.com/lycaon/lycaon/internal/modelfeed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestConfiguredContextCapSurvivesCatalogResolution(t *testing.T) {
	for _, kind := range []string{"ollama", "openai", "azure", "bedrock", "vertex", "vertex-express"} {
		t.Run(kind, func(t *testing.T) {
			for _, tc := range []struct {
				name                  string
				cap, advertised, want int
			}{
				{"bounded", 32768, 262144, 32768},
				{"refreshed", 32768, 524288, 32768},
				{"uncapped", 0, 262144, 262144},
				{"cannot expand capacity", 32768, 16384, 16384},
				{"unknown capacity", 32768, 0, 32768},
			} {
				t.Run(tc.name, func(t *testing.T) {
					local := []modelinfo.Entry{{ID: "model", ContextLength: tc.cap, MaxTokens: 1024}}
					discovered := []modelinfo.Entry{{ID: "model", ContextLength: tc.advertised,
						Capabilities: modelinfo.ModelCapabilities{Tools: modelinfo.Evidence(modelinfo.CapabilitySupported, "probe")}}}
					key, _ := modelfeed.FeedKeyForKind(kind)
					doc := &modelfeed.Document{Providers: map[string]modelfeed.Provider{
						key: {ID: key, Models: map[string]modelfeed.Model{"model": {
							ID: "model", ToolCall: new(true), Modalities: modelfeed.Modalities{Output: []string{"text"}},
						}}},
					}}
					got := mergeAssignableModels(kind, local, discovered, nil, doc, modelfeed.StatusOK, true)
					if len(got.Models) != 1 || got.Models[0].ContextLength != tc.want || got.Models[0].MaxTokens != 1024 {
						t.Fatalf("resolved models = %+v, want context %d and output limit 1024", got.Models, tc.want)
					}
					if local[0].ContextLength != tc.cap || discovered[0].ContextLength != tc.advertised {
						t.Fatal("resolution mutated source metadata")
					}
				})
			}
		})
	}
}

func TestOllamaContextCapSurvivesRuntimeRefresh(t *testing.T) {
	local := []modelinfo.Entry{{ID: "model", ContextLength: 32768}}
	p := ollamaprovider.New("remote", "http://unused.invalid", "", local)
	for _, capacity := range []int{262144, 524288} {
		merged := mergeAssignableModels("ollama", local,
			[]modelinfo.Entry{{ID: "model:latest", ContextLength: capacity}}, nil, nil, "", false)
		p = p.WithEffectiveModels(merged.Models)
		if got := p.ContextLimit(t.Context(), "model:latest"); got != 32768 {
			t.Fatalf("refreshed context = %d, want 32768", got)
		}
		req := modelcall.CompletionRequest{Model: "model:latest", MaxTokens: 1024,
			Messages: []api.Message{{Role: api.MessageRoleUser, Content: strings.Repeat("a", 60000)}}}
		body, _, err := p.Prepare(t.Context(), req, false)
		testutil.FailErr(t, "build capped request", err)
		if body.Options.NumCtx != 32768 {
			t.Fatalf("request context = %d, want 32768", body.Options.NumCtx)
		}
		req.Messages[0].Content = strings.Repeat("a", 140000)
		_, _, err = p.Prepare(t.Context(), req, false)
		var tooSmall *failure.ProviderContextTooSmallError
		if !errors.As(err, &tooSmall) || tooSmall.MaxContext != 32768 {
			t.Fatalf("oversize request = %v, want capped-context refusal", err)
		}
	}
}
