package llm

import (
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
)

func withThinkingRules(t *testing.T, rules []modelinfo.ThinkingRule) {
	t.Helper()
	modelinfo.SetThinkingRules(rules)
	t.Cleanup(func() { modelinfo.SetThinkingRules(nil) })
}

func bundledThinkingRules(t *testing.T) []modelinfo.ThinkingRule {
	t.Helper()
	cfg, err := LoadProviderConfig()
	if err != nil {
		t.Fatalf("load bundled providers: %v", err)
	}
	return cfg.ModelThinking
}

func TestBundledModelThinkingRulesValid(t *testing.T) {
	rules := bundledThinkingRules(t)
	if len(rules) == 0 {
		t.Fatal("bundled providers.yaml has no model_thinking rules")
	}
	if err := modelinfo.ValidateThinkingRules(rules); err != nil {
		t.Fatalf("bundled rules invalid: %v", err)
	}
}

func TestParseThinkStyleCoversAllConstants(t *testing.T) {
	// Parser ↔ enum drift guard: every declared style string must parse.
	for _, style := range []modelinfo.ThinkStyle{
		modelinfo.ThinkStyleNone, modelinfo.ThinkStyleEffortLevels, modelinfo.ThinkStyleThinkingType,
		modelinfo.ThinkStyleBooleanThink, modelinfo.ThinkStyleBudgetTokens, modelinfo.ThinkStyleAdaptive,
	} {
		got, ok := modelinfo.ParseThinkStyle(string(style))
		if !ok || got != style {
			t.Fatalf("parseThinkStyle(%q) = (%q, %v)", style, got, ok)
		}
	}
	if _, ok := modelinfo.ParseThinkStyle("bogus"); ok {
		t.Fatal("parseThinkStyle accepted an unknown style")
	}
}

func TestValidateThinkingRulesRejectsBadRules(t *testing.T) {
	if err := modelinfo.ValidateThinkingRules([]modelinfo.ThinkingRule{{Match: []string{"x"}, Style: "bogus"}}); err == nil {
		t.Fatal("unknown style accepted")
	}
	if err := modelinfo.ValidateThinkingRules([]modelinfo.ThinkingRule{{Style: "none"}}); err == nil {
		t.Fatal("empty match list accepted")
	}
	if err := modelinfo.ValidateThinkingRules([]modelinfo.ThinkingRule{{Match: []string{" "}, Style: "none"}}); err == nil {
		t.Fatal("blank pattern accepted")
	}
	if err := modelinfo.ValidateThinkingRules([]modelinfo.ThinkingRule{{Match: []string{"x"}, Style: "budget_tokens", DefaultOn: true}}); err == nil {
		t.Fatal("default_on accepted for a non-adaptive protocol")
	}
}

func TestResolveModelThinkingFamilyRuleOnAggregator(t *testing.T) {
	withThinkingRules(t, bundledThinkingRules(t))

	// Family rules override the transport's thinking defaults.
	profile := providerprofile.Fireworks()
	mt := modelcall.ResolveModelThinking(profile, modelinfo.Entry{}, "accounts/fireworks/models/kimi-k2p6")
	if mt.Style != modelinfo.ThinkStyleThinkingType || mt.AlwaysOn {
		t.Fatalf("kimi-k2p6 = %+v want thinking_type", mt)
	}

	// Code requires reasoning even when a utility requests it disabled.
	mt = modelcall.ResolveModelThinking(profile, modelinfo.Entry{}, "accounts/fireworks/models/kimi-k2p7-code")
	if mt.Style != modelinfo.ThinkStyleThinkingType || !mt.AlwaysOn {
		t.Fatalf("kimi-k2p7-code = %+v want always-on thinking_type", mt)
	}

	// The instruct line has no reasoning mode at all.
	mt = modelcall.ResolveModelThinking(profile, modelinfo.Entry{}, "moonshotai/Kimi-K2-Instruct")
	if mt.Style != modelinfo.ThinkStyleNone {
		t.Fatalf("kimi instruct = %+v want none", mt)
	}

	// gpt-oss keeps the effort vocabulary.
	mt = modelcall.ResolveModelThinking(profile, modelinfo.Entry{}, "@cf/openai/gpt-oss-120b")
	if mt.Style != modelinfo.ThinkStyleEffortLevels {
		t.Fatalf("gpt-oss = %+v want effort_levels", mt)
	}
}

func TestResolveModelThinkingTransportConstraint(t *testing.T) {
	withThinkingRules(t, bundledThinkingRules(t))

	// Budget controls project to the gateway reasoning object.
	mt := modelcall.ResolveModelThinking(providerprofile.Openrouter(), modelinfo.Entry{}, "anthropic/claude-sonnet-4-6")
	if mt.Style != modelinfo.ThinkStyleReasoningObject {
		t.Fatalf("claude on openrouter = %+v want reasoning_object fallback", mt)
	}

	// Provider-managed adaptive thinking is preserved.
	mt = modelcall.ResolveModelThinking(providerprofile.Anthropic(), modelinfo.Entry{}, "claude-sonnet-4-6")
	if mt.Style != modelinfo.ThinkStyleAdaptive {
		t.Fatalf("claude on anthropic = %+v want adaptive", mt)
	}

	// Native boolean thinking remains transport-specific.
	mt = modelcall.ResolveModelThinking(providerprofile.Ollama(), modelinfo.Entry{}, "kimi-k2p6:cloud")
	if mt.Style != modelinfo.ThinkStyleBooleanThink {
		t.Fatalf("kimi on ollama = %+v want boolean_think fallback", mt)
	}
}

func TestResolveModelThinkingPrecedence(t *testing.T) {
	withThinkingRules(t, []modelinfo.ThinkingRule{
		{Match: []string{"mymodel"}, Style: "thinking_type", AlwaysOn: true},
	})
	profile := providerprofile.OpenAI()

	// Per-model entry override beats the family rule on style but still
	// inherits the family always-on quirk.
	mt := modelcall.ResolveModelThinking(profile, modelinfo.Entry{ThinkStyle: "effort_levels"}, "mymodel-v2")
	if mt.Style != modelinfo.ThinkStyleEffortLevels || !mt.AlwaysOn {
		t.Fatalf("entry override = %+v want effort_levels always-on", mt)
	}

	// Entry always_on merges with rule-less models.
	mt = modelcall.ResolveModelThinking(profile, modelinfo.Entry{ThinkingAlwaysOn: true}, "other-model")
	if mt.Style != modelinfo.ThinkStyleEffortLevels || !mt.AlwaysOn {
		t.Fatalf("entry always-on = %+v", mt)
	}

	// No rule, no entry: transport default.
	mt = modelcall.ResolveModelThinking(profile, modelinfo.Entry{}, "plain-model")
	if mt.Style != modelinfo.ThinkStyleEffortLevels || mt.AlwaysOn {
		t.Fatalf("default = %+v", mt)
	}
}

func TestResolveModelThinkingFirstMatchWins(t *testing.T) {
	withThinkingRules(t, []modelinfo.ThinkingRule{
		{Match: []string{"special-model"}, Style: "none"},
		{Match: []string{"model"}, Style: "thinking_type"},
	})
	profile := providerprofile.OpenAI()
	if mt := modelcall.ResolveModelThinking(profile, modelinfo.Entry{}, "special-model-1"); mt.Style != modelinfo.ThinkStyleNone {
		t.Fatalf("specific rule lost: %+v", mt)
	}
	if mt := modelcall.ResolveModelThinking(profile, modelinfo.Entry{}, "generic-model-1"); mt.Style != modelinfo.ThinkStyleThinkingType {
		t.Fatalf("catch-all rule lost: %+v", mt)
	}
}
