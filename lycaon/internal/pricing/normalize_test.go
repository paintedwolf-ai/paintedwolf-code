package pricing

import (
	"reflect"
	"testing"
)

func TestNormalizeRateModelID(t *testing.T) {
	cases := []struct {
		kind, id, want string
	}{
		{"openai", "  GPT-4.1  ", "gpt-4.1"},
		{"gemini", "models/gemini-2.5-pro", "gemini-2.5-pro"},
		{"gemini", "gemini-2.5-pro", "gemini-2.5-pro"},
		{"vertex", "google/gemini-2.5-pro", "gemini-2.5-pro"},
		{"vertex", "gemini-2.5-pro", "gemini-2.5-pro"},
		{"bedrock", "us.anthropic.claude-sonnet-4-20250514-v1:0", "anthropic.claude-sonnet-4-20250514-v1:0"},
		{"bedrock", "us-gov.anthropic.claude-sonnet-4-20250514-v1:0", "anthropic.claude-sonnet-4-20250514-v1:0"},
		{"bedrock", "apac.amazon.nova-pro-v1:0", "amazon.nova-pro-v1:0"},
		{"bedrock", "anthropic.claude-sonnet-4-20250514-v1:0", "anthropic.claude-sonnet-4-20250514-v1:0"},
		// Equivalent billing kinds share model normalization.
		{"vertex-express", "models/gemini-2.5-pro", "gemini-2.5-pro"},
		{"vertex-express", "gemini-2.5-pro", "gemini-2.5-pro"},
		// Other kinds keep composite IDs.
		{"openrouter", "anthropic/claude-sonnet-4", "anthropic/claude-sonnet-4"},
	}
	for _, c := range cases {
		if got := NormalizeRateModelID(c.kind, c.id); got != c.want {
			t.Fatalf("NormalizeRateModelID(%q, %q) = %q want %q", c.kind, c.id, got, c.want)
		}
	}
}

// Equivalent billing kinds share a rate key.
func TestRateKindCollapsesBillingAliases(t *testing.T) {
	cases := []struct{ kind, want string }{
		{"vertex-express", "gemini"},
		{"gemini", "gemini"},
		{"  vertex-express  ", "gemini"},
		{"vertex", "vertex"},
		{"anthropic", "anthropic"},
		{"", ""},
	}
	for _, c := range cases {
		if got := RateKind(c.kind); got != c.want {
			t.Errorf("RateKind(%q) = %q, want %q", c.kind, got, c.want)
		}
	}
}

func TestRateLookupCandidates(t *testing.T) {
	got := RateLookupCandidates("anthropic", "claude-sonnet-4-20250514")
	want := []string{"claude-sonnet-4-20250514", "claude-sonnet-4"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("candidates = %v want %v", got, want)
	}

	got = RateLookupCandidates("ollama", "llama3.2:8b")
	want = []string{"llama3.2:8b", "llama3.2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("candidates = %v want %v", got, want)
	}

	got = RateLookupCandidates("openai", "gpt-4.1")
	want = []string{"gpt-4.1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("candidates = %v want %v", got, want)
	}
}
