package providerprofile

import (
	"errors"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/failure"
)

func TestToolCallRejectionPolicyValidation(t *testing.T) {
	valid := ToolCallRejectionRule{Kind: ToolCallArgumentsUnparsed, Grammar: "unexpected end of JSON input", Source: "parser.go"}
	for _, tc := range []struct {
		name   string
		policy ToolCallRejectionPolicy
		want   string
	}{
		{"no rules", ToolCallRejectionPolicy{}, "no rules"},
		{"status", ToolCallRejectionPolicy{Rules: []ToolCallRejectionRule{valid}, StatusBeforeOutput: 200}, "not an HTTP error status"},
		{"kind", ToolCallRejectionPolicy{Rules: []ToolCallRejectionRule{{Kind: "tool_named", Grammar: "x", Source: "s"}}}, "unsupported kind"},
		{"source", ToolCallRejectionPolicy{Rules: []ToolCallRejectionRule{{Kind: ToolCallArgumentsUnparsed, Grammar: "x"}}}, "cites no source"},
		{"grammar", ToolCallRejectionPolicy{Rules: []ToolCallRejectionRule{{Kind: ToolCallArgumentsUnparsed, Grammar: "(", Source: "s"}}}, "grammar"},
		{"capture", ToolCallRejectionPolicy{Rules: []ToolCallRejectionRule{{Kind: ToolCallArgumentsUnparsed, Grammar: "tool '(.+)'", Source: "s"}}}, "captures"},
	} {
		if err := tc.policy.Compile(); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error = %v, want %q", tc.name, err, tc.want)
		}
	}
	if err := ResolveToolCallRejectionProfiles(map[string]ToolCallRejectionPolicy{" ": {Rules: []ToolCallRejectionRule{valid}}}); err == nil {
		t.Fatal("empty profile name accepted")
	}
}

// A grammar spans the whole message even when it alternates, and nothing
// matches before the policy is compiled.
func TestToolCallRejectionPolicyMatchesWholeMessages(t *testing.T) {
	policy := ToolCallRejectionPolicy{Rules: []ToolCallRejectionRule{{Kind: ToolCallArgumentsUnparsed, Grammar: "a|b", Source: "s"}}}
	if _, ok := policy.Match("a"); ok {
		t.Fatal("uncompiled policy matched")
	}
	profiles := map[string]ToolCallRejectionPolicy{"p": policy}
	if err := ResolveToolCallRejectionProfiles(profiles); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	policy = profiles["p"]
	if policy.Profile != "p" {
		t.Fatalf("profile name = %q", policy.Profile)
	}
	for message, want := range map[string]bool{"a": true, "b": true, "ab": false, "xa": false, "b\n": false} {
		if _, got := policy.Match(message); got != want {
			t.Errorf("Match(%q) = %v, want %v", message, got, want)
		}
	}
	cause := errors.New("provider: a")
	if got := policy.Classify("p", "m", "a", false, cause); got != cause {
		t.Fatal("ineligible request was typed")
	}
	if got := policy.Classify("p", "m", "c", true, cause); got != cause {
		t.Fatal("unmatched message was typed")
	}
	typed, ok := failure.AsProviderToolCallUnparsed(policy.Classify("p", "m", "a", true, cause))
	if !ok || typed.ProviderID != "p" || typed.Model != "m" || typed.Cause != cause {
		t.Fatalf("typed = %+v", typed)
	}
}
