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
		{"kind", ToolCallRejectionPolicy{Rules: []ToolCallRejectionRule{{Kind: "tool_renamed", Grammar: "x", Source: "s"}}}, "unsupported kind"},
		{"source", ToolCallRejectionPolicy{Rules: []ToolCallRejectionRule{{Kind: ToolCallArgumentsUnparsed, Grammar: "x"}}}, "cites no source"},
		{"grammar", ToolCallRejectionPolicy{Rules: []ToolCallRejectionRule{{Kind: ToolCallArgumentsUnparsed, Grammar: "(", Source: "s"}}}, "grammar"},
		{"capture", ToolCallRejectionPolicy{Rules: []ToolCallRejectionRule{{Kind: ToolCallArgumentsUnparsed, Grammar: "tool '(.+)'", Source: "s"}}}, "captures"},
		{"no name", ToolCallRejectionPolicy{Rules: []ToolCallRejectionRule{{Kind: ToolCallNotOffered, Grammar: "tool not found", Source: "s"}}}, "captures"},
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
	if got := policy.Classify("p", "m", "a", false, cause); got != cause { //nolint:errorlint // identity: an unclassified cause is returned itself
		t.Fatal("ineligible request was typed")
	}
	if got := policy.Classify("p", "m", "c", true, cause); got != cause { //nolint:errorlint // identity: an unclassified cause is returned itself
		t.Fatal("unmatched message was typed")
	}
	typed, ok := failure.AsProviderToolCallUnparsed(policy.Classify("p", "m", "a", true, cause))
	if !ok || typed.ProviderID != "p" || typed.Model != "m" || !errors.Is(typed.Cause, cause) {
		t.Fatalf("typed = %+v", typed)
	}
}

// A tool_not_offered rejection yields only a token name outside the offered
// set; offered names, non-token captures, and other kinds stay errors.
func TestToolCallRejectionPolicyUnofferedTool(t *testing.T) {
	profiles := map[string]ToolCallRejectionPolicy{"p": {Rules: []ToolCallRejectionRule{
		{Kind: ToolCallNotOffered, Grammar: `tool '([^']*)' not found`, Source: "s"},
		{Kind: ToolCallArgumentsUnparsed, Grammar: "unexpected end of JSON input", Source: "s"},
	}}}
	if err := ResolveToolCallRejectionProfiles(profiles); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	policy, offered := profiles["p"], []string{"read"}
	for message, want := range map[string]string{
		"tool 'edit' not found":                            "edit",
		"tool 'read' not found":                            "",
		"tool 'ed it' not found":                           "",
		"tool '' not found":                                "",
		"tool '" + strings.Repeat("a", 65) + "' not found": "",
		"unexpected end of JSON input":                     "",
	} {
		got, ok := policy.UnofferedTool(message, offered)
		if (got != "") != ok || got != want {
			t.Errorf("UnofferedTool(%q) = %q, %v; want %q", message, got, ok, want)
		}
	}
	if _, ok := policy.UnofferedTool("tool 'edit' not found", nil); ok {
		t.Fatal("recovered without an offered set")
	}
	for message, want := range map[string]bool{"tool 'edit' not found": true, "tool 'read' not found": false, "unexpected end of JSON input": true, "other": false} {
		if got := policy.Recognizes(message, offered); got != want {
			t.Errorf("Recognizes(%q) = %v, want %v", message, got, want)
		}
	}
}
