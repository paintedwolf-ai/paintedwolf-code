package openaicompat

import (
	"encoding/json"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestDeclaredReasoningOffSurvivesWireEncoding(t *testing.T) {
	for _, profile := range []providerprofile.Profile{providerprofile.Fireworks(), providerprofile.OpenAIVendor(), providerprofile.Gemini()} {
		p := New("fixture", "https://example.invalid", "", []modelinfo.Entry{{ID: "hybrid", ReasoningEffort: "none"}}).WithProfile(profile)
		for _, stream := range []bool{false, true} {
			for _, structured := range []bool{false, true} {
				req := modelcall.CompletionRequest{Model: "hybrid"}
				if structured {
					req.ResponseFormat = &modelcall.ResponseFormat{Type: modelcall.ResponseFormatJSONObject}
				}
				body, err := encodeChatCompletionRequest(req, p, stream, controlOpts{})
				testutil.FailErr(t, "encode disabled reasoning", err)
				var wire map[string]any
				testutil.FailErr(t, "decode disabled reasoning", json.Unmarshal(body, &wire))
				if wire["reasoning_effort"] != "none" {
					t.Fatalf("stream=%v structured=%v: disabled reasoning lost: %s", stream, structured, body)
				}
			}
		}
	}
}

func TestReasoningOffPolicyAndBudget(t *testing.T) {
	withThinkingRules(t, bundledThinkingRules(t))
	for _, tc := range []struct {
		name, model string
		req         modelcall.CompletionRequest
		opts        controlOpts
		want        string
	}{
		{name: "explicit off", model: "hybrid", req: modelcall.CompletionRequest{Think: modelcall.ThinkOff}, want: "none"},
		{name: "utility default", model: "hybrid", req: modelcall.CompletionRequest{Composition: modelcall.CompositionHostUtility}, want: "none"},
		{name: "strict", model: "hybrid", opts: controlOpts{strictRetry: true}, want: "none"},
		{name: "fallback", model: "hybrid", opts: controlOpts{reasoning: reasoningFallbackOff}, want: "none"},
		{name: "omitted", model: "hybrid", opts: controlOpts{reasoning: reasoningFallbackOmit}},
		{name: "explicit low", model: "hybrid", req: modelcall.CompletionRequest{Think: modelcall.ThinkLow}, want: "low"},
		{name: "always on", model: "accounts/fireworks/models/glm-5p3-flash", req: modelcall.CompletionRequest{Think: modelcall.ThinkOff}, want: "low"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := New("fixture", "https://example.invalid", "", nil).WithProfile(providerprofile.Fireworks())
			tc.req.Model = tc.model
			tc.req.MaxTokens = 2048
			got := p.resolveRequestControls(tc.req, tc.model, tc.opts)
			if got.ReasoningEffort != tc.want {
				t.Fatalf("effort=%q, want %q", got.ReasoningEffort, tc.want)
			}
			if tc.want == "none" && got.MaxTokens != 2048 {
				t.Fatalf("disabled reasoning enlarged output budget: %d", got.MaxTokens)
			}
		})
	}
}
