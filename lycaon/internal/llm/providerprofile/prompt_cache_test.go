package providerprofile

import (
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/pkg/api"
	"gopkg.in/yaml.v3"
)

func TestPromptCachePolicyValidateRejectsFieldsItsModeCannotRead(t *testing.T) {
	cases := []struct {
		name   string
		policy PromptCachePolicy
		fault  string
	}{
		{"unknown mode", PromptCachePolicy{Mode: "sometimes"}, "unsupported mode"},
		{"session key off automatic", PromptCachePolicy{Mode: PromptCacheLocalKV, SessionKey: true}, "need mode automatic_prefix"},
		{"request marker without cache_control", PromptCachePolicy{Mode: PromptCacheAutomaticPrefix, Marker: PromptCacheMarkerBreakpoint, RequestMarker: true}, "needs marker cache_control"},
		{"keep alive off local", PromptCachePolicy{Mode: PromptCacheAutomaticPrefix, KeepAlive: "24h"}, "need mode local_kv"},
		{"explicit with one tier named", PromptCachePolicy{Mode: PromptCacheExplicitBreakpoints, Lifetime: PromptCacheLifetime{Standing: CacheDuration(time.Hour)}}, "must be 5m or 1h"},
		{"explicit with an unsupported lifetime", PromptCachePolicy{Mode: PromptCacheExplicitBreakpoints, Lifetime: PromptCacheLifetime{Standing: CacheDuration(30 * time.Minute), History: CacheDuration(5 * time.Minute)}}, "must be 5m or 1h"},
		{"history outliving standing", PromptCachePolicy{Mode: PromptCacheExplicitBreakpoints, Lifetime: PromptCacheLifetime{Standing: CacheDuration(5 * time.Minute), History: CacheDuration(time.Hour)}}, "must not be shorter"},
		{"cold_after beside lifetimes", PromptCachePolicy{Mode: PromptCacheExplicitBreakpoints, Lifetime: PromptCacheLifetime{Standing: CacheDuration(time.Hour), History: CacheDuration(5 * time.Minute)}, ColdAfter: CacheDuration(time.Minute)}, "cold_after is the lifetime"},
		{"lifetime off explicit", PromptCachePolicy{Mode: PromptCacheAutomaticPrefix, Lifetime: PromptCacheLifetime{Standing: CacheDuration(time.Hour)}}, "lifetime needs mode explicit_breakpoints"},
		{"cold_after without caching", PromptCachePolicy{Mode: PromptCacheNone, ColdAfter: CacheDuration(time.Minute)}, "needs a caching mode"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.policy.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.fault) {
				t.Fatalf("Validate() = %v, want fault %q", err, tc.fault)
			}
		})
	}
}

// A provider that fixes each checkpoint's lifetime takes explicit breakpoints
// with no lifetime named; cold_after then carries the provider's figure.
func TestPromptCachePolicyExplicitBreakpointsWithProviderFixedLifetime(t *testing.T) {
	policy := PromptCachePolicy{Mode: PromptCacheExplicitBreakpoints, ColdAfter: CacheDuration(5 * time.Minute)}
	if err := policy.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want an unset lifetime accepted", err)
	}
	if got := policy.StandingColdAfter(); got != 5*time.Minute {
		t.Fatalf("StandingColdAfter() = %v, want cold_after when no lifetime is named", got)
	}
	if got := policy.Lifetime.For(api.PromptCacheTierStanding); got != 0 {
		t.Fatalf("standing lifetime = %v, want none on the wire", got)
	}
}

func TestPromptCacheRulesRefineTheProfilesTheyName(t *testing.T) {
	breakpoint := PromptCacheMarkerBreakpoint
	thirty := CacheDuration(30 * time.Minute)
	none := PromptCacheNone
	openai := PromptCachePolicy{Profile: "openai", Mode: PromptCacheAutomaticPrefix, SessionKey: true, ColdAfter: CacheDuration(10 * time.Minute)}
	other := PromptCachePolicy{Profile: "other", Mode: PromptCacheAutomaticPrefix}
	SetPromptCacheRules([]PromptCacheRule{
		{Profiles: []string{"openai"}, Match: []string{"GPT-5.6"}, Marker: &breakpoint, ColdAfter: &thirty},
		{Profiles: []string{"openai"}, Exact: []string{"gpt-5"}, Mode: &none},
		{Profiles: []string{"openai"}, Match: []string{"gpt-5"}, ColdAfter: &thirty},
	})
	t.Cleanup(func() { SetPromptCacheRules(nil) })

	if got := openai.ForModel("gpt-5.6-sol"); got.Marker != PromptCacheMarkerBreakpoint || got.ColdAfter != thirty || !got.SessionKey {
		t.Fatalf("matched rule = %+v", got)
	}
	if got := openai.ForModel("gpt-5"); got != (PromptCachePolicy{Profile: "openai", Mode: PromptCacheNone}) {
		t.Fatalf("a new mode keeps none of the old mode's fields, got %+v", got)
	}
	if got := openai.ForModel("gpt-4o"); got != openai {
		t.Fatalf("unmatched model keeps the profile, got %+v", got)
	}
	if got := other.ForModel("gpt-5.6-sol"); got != other {
		t.Fatalf("a rule for another profile applies, got %+v", got)
	}
}

func TestValidatePromptCacheChecksEveryProfileARuleNames(t *testing.T) {
	marker := PromptCacheMarkerBreakpoint
	profiles := map[string]PromptCachePolicy{
		"automatic": {Mode: PromptCacheAutomaticPrefix},
		"local":     {Mode: PromptCacheLocalKV},
	}
	if err := ValidatePromptCache(profiles, []PromptCacheRule{{Profiles: []string{"automatic"}, Match: []string{"x"}, Marker: &marker}}); err != nil {
		t.Fatalf("valid rule rejected: %v", err)
	}
	cases := []struct {
		name  string
		rule  PromptCacheRule
		fault string
	}{
		{"no profiles", PromptCacheRule{Match: []string{"x"}}, "profiles is required"},
		{"no match", PromptCacheRule{Profiles: []string{"automatic"}}, "match or exact is required"},
		{"unknown profile", PromptCacheRule{Profiles: []string{"missing"}, Match: []string{"x"}}, "unknown profile"},
		{"marker on local", PromptCacheRule{Profiles: []string{"automatic", "local"}, Match: []string{"x"}, Marker: &marker}, `on "local"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidatePromptCache(profiles, []PromptCacheRule{tc.rule})
			if err == nil || !strings.Contains(err.Error(), tc.fault) {
				t.Fatalf("ValidatePromptCache() = %v, want fault %q", err, tc.fault)
			}
		})
	}
}

func TestPromptCachePolicyDecodesDurations(t *testing.T) {
	var policy PromptCachePolicy
	if err := yaml.Unmarshal([]byte("mode: explicit_breakpoints\nlifetime: {standing: 1h, history: 5m}\n"), &policy); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if policy.Lifetime.Standing.Duration() != time.Hour || policy.StandingColdAfter() != time.Hour {
		t.Fatalf("standing = %v, cold after %v", policy.Lifetime.Standing.Duration(), policy.StandingColdAfter())
	}
	if err := yaml.Unmarshal([]byte("cold_after: soon\n"), &policy); err == nil {
		t.Fatal("unparseable duration accepted")
	}
}
