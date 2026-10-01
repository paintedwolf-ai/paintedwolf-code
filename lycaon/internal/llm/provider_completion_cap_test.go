package llm

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providers/openaicompat"
	"github.com/lycaon/lycaon/internal/testutil"
)

// Factory-built profiles select the completion-cap field for each provider kind.
func TestCompletionCapFieldByProviderKind(t *testing.T) {
	cases := []struct {
		kind      string
		wantField string
		deadField string
	}{
		{kind: "openai", wantField: "max_completion_tokens", deadField: "max_tokens"},
		{kind: "openai-compatible", wantField: "max_tokens", deadField: "max_completion_tokens"},
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			built, err := driverFactories.Build(context.Background(), tc.kind, providerBuild{
				entry: CatalogEntry{
					ID:      tc.kind,
					Kind:    tc.kind,
					BaseURL: "http://localhost/v1",
					Models:  []modelinfo.Entry{{ID: "m", MaxTokens: 8192}},
				},
				apiKey: "k",
			})
			testutil.FailErr(t, "build provider", err)
			provider, ok := built.(*openaicompat.Provider)
			if !ok {
				t.Fatalf("kind %q built %T, want *OpenAIProvider", tc.kind, built)
			}

			body, err := provider.Prepare(modelcall.CompletionRequest{Model: "m"}, false)
			testutil.FailErr(t, "encode request", err)
			var wire map[string]any
			testutil.FailErr(t, "decode request", json.Unmarshal(body, &wire))

			if wire[tc.wantField] != float64(8192) {
				t.Fatalf("%s = %v, want 8192; body=%s", tc.wantField, wire[tc.wantField], body)
			}
			if _, exists := wire[tc.deadField]; exists {
				t.Fatalf("kind %q emitted %s: %s", tc.kind, tc.deadField, body)
			}
		})
	}
}
