package openaicompat

import (
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
)

// shippedPromptCache returns the prompt-cache policy the provider kind
// catalog gives kind and installs the catalog's model rules, so encoding
// tests exercise the policy that ships.
func shippedPromptCache(t *testing.T, kind string) providerprofile.PromptCachePolicy {
	t.Helper()
	data, err := config.Read(config.Providers)
	if err != nil {
		t.Fatalf("read provider kind catalog: %v", err)
	}
	var catalog struct {
		Profiles  map[string]providerprofile.PromptCachePolicy `yaml:"prompt_cache_profiles"`
		Rules     []providerprofile.PromptCacheRule            `yaml:"model_prompt_cache"`
		Providers []struct {
			Kind    string `yaml:"kind"`
			Profile string `yaml:"prompt_cache_profile"`
		} `yaml:"providers"`
	}
	if err := yaml.Unmarshal(data, &catalog); err != nil {
		t.Fatalf("parse provider kind catalog: %v", err)
	}
	if err := providerprofile.ValidatePromptCache(catalog.Profiles, catalog.Rules); err != nil {
		t.Fatalf("validate prompt cache: %v", err)
	}
	providerprofile.SetPromptCacheRules(catalog.Rules)
	t.Cleanup(func() { providerprofile.SetPromptCacheRules(nil) })
	for _, p := range catalog.Providers {
		if p.Kind == kind {
			policy := catalog.Profiles[p.Profile]
			policy.Profile = p.Profile
			return policy
		}
	}
	t.Fatalf("no provider kind %q", kind)
	return providerprofile.PromptCachePolicy{}
}
