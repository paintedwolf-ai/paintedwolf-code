package llm

import (
	"net/http"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/providerretry"
)

func hostedRetryProfileForTest() providerHTTPRetryProfile {
	return providerHTTPRetryProfile{
		ServiceClass: providerServiceClassHosted,
		HTTPRetry: providerretry.ProviderHTTPRetry{
			MaxRetries:  1,
			MaxWaitMs:   1000,
			BackoffMs:   []int{1},
			Statuses:    []int{429, 500, 502, 504},
			WaitHeaders: []string{"Retry-After"},
			Capacity: &providerretry.ProviderHTTPRetry{
				MaxRetries:  1,
				MaxWaitMs:   1000,
				BackoffMs:   []int{1},
				Statuses:    []int{503},
				WaitHeaders: []string{"Retry-After"},
			},
		},
	}
}

func TestResolveProviderHTTPRetryProfiles(t *testing.T) {
	cfg := ProviderConfig{
		HTTPRetryProfiles: map[string]providerHTTPRetryProfile{
			"hosted": hostedRetryProfileForTest(),
		},
		Providers: []ProviderEntry{{ID: "gateway", HTTPRetryProfile: "hosted"}},
	}
	if err := resolveProviderHTTPRetryProfiles(&cfg); err != nil {
		t.Fatalf("resolveProviderHTTPRetryProfiles: %v", err)
	}
	policy := cfg.Providers[0].HTTPRetry
	for _, status := range []int{429, 500, 502, 504} {
		if !policy.AllowsStatus(status) {
			t.Errorf("resolved policy does not retry HTTP %d", status)
		}
	}
	policy.Statuses[0] = http.StatusTeapot
	if cfg.HTTPRetryProfiles["hosted"].HTTPRetry.Statuses[0] == http.StatusTeapot {
		t.Fatal("resolved policy aliases the profile")
	}
}

func TestResolveProviderHTTPRetryProfilesRejectsInvalidReferences(t *testing.T) {
	profile := hostedRetryProfileForTest()
	for _, tc := range []struct {
		name string
		cfg  ProviderConfig
	}{
		{
			name: "unknown profile",
			cfg:  ProviderConfig{Providers: []ProviderEntry{{ID: "p", HTTPRetryProfile: "missing"}}},
		},
		{
			name: "profile and inline policy",
			cfg: ProviderConfig{
				HTTPRetryProfiles: map[string]providerHTTPRetryProfile{"hosted": profile},
				Providers: []ProviderEntry{{
					ID: "p", HTTPRetryProfile: "hosted", HTTPRetryOverride: &profile.HTTPRetry,
				}},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := resolveProviderHTTPRetryProfiles(&tc.cfg); err == nil {
				t.Fatal("invalid retry profile configuration was accepted")
			}
		})
	}
}

func TestHostedRetryProfileRequiresTransientStatusSet(t *testing.T) {
	for _, status := range []int{429, 500, 502, 504} {
		profile := hostedRetryProfileForTest()
		filtered := profile.HTTPRetry.Statuses[:0]
		for _, candidate := range profile.HTTPRetry.Statuses {
			if candidate != status {
				filtered = append(filtered, candidate)
			}
		}
		profile.HTTPRetry.Statuses = filtered
		if err := validateProviderHTTPRetryProfile("hosted", profile); err == nil {
			t.Errorf("hosted profile without HTTP %d was accepted", status)
		}
	}
}

func TestEveryShippedProviderUsesAResolvedRetryProfile(t *testing.T) {
	cfg, err := LoadProviderConfig()
	if err != nil {
		t.Fatalf("LoadProviderConfig: %v", err)
	}
	for _, provider := range cfg.Providers {
		if provider.HTTPRetryProfile == "" {
			t.Errorf("provider %s uses an inline retry policy", provider.ID)
		}
		if provider.HTTPRetry.IsZero() {
			t.Errorf("provider %s has no resolved retry policy", provider.ID)
		}
	}

	together := providerByID(cfg.Providers, "together")
	for _, status := range []int{500, 502, 504} {
		if together == nil || !together.HTTPRetry.AllowsStatus(status) {
			t.Errorf("Together AI does not retry HTTP %d", status)
		}
	}
}

func TestLocalEmptyRetryOverrideIsRejectedInsteadOfInheriting(t *testing.T) {
	empty := providerretry.ProviderHTTPRetry{}
	_, err := resolveLocalEntry(
		ProviderEntry{
			ID:                "gateway-1",
			Kind:              "gateway",
			HTTPRetryOverride: &empty,
		},
		map[string]ProviderEntry{
			"gateway": {
				Kind:      "gateway",
				HTTPRetry: hostedRetryProfileForTest().HTTPRetry,
			},
		},
	)
	if err == nil {
		t.Fatal("empty local retry override silently inherited the kind profile")
	}
}

func providerByID(providers []ProviderEntry, id string) *ProviderEntry {
	for i := range providers {
		if providers[i].ID == id {
			return &providers[i]
		}
	}
	return nil
}
