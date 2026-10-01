package llm

import (
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/llm/providerretry"
)

// providerServiceClass states which retry invariants a named profile obeys.
type providerServiceClass string

const (
	providerServiceClassHosted providerServiceClass = "hosted"
	providerServiceClassLocal  providerServiceClass = "local"
	providerServiceClassSDK    providerServiceClass = "sdk_managed"
)

// providerHTTPRetryProfile is a reusable retry policy for provider kinds.
type providerHTTPRetryProfile struct {
	ServiceClass providerServiceClass            `yaml:"service_class"`
	HTTPRetry    providerretry.ProviderHTTPRetry `yaml:"http_retry"`
}

// resolveProviderHTTPRetryProfiles validates profiles and projects them onto
// provider entries. A provider may use a named profile or a complete inline
// policy, never both.
func resolveProviderHTTPRetryProfiles(cfg *ProviderConfig) error {
	if cfg == nil {
		return nil
	}
	for name, profile := range cfg.HTTPRetryProfiles {
		if err := validateProviderHTTPRetryProfile(name, profile); err != nil {
			return err
		}
	}
	for i := range cfg.Providers {
		entry := &cfg.Providers[i]
		profileName := strings.TrimSpace(entry.HTTPRetryProfile)
		if profileName == "" {
			if entry.HTTPRetryOverride != nil {
				if err := providerretry.ValidateHTTPRetry(*entry.HTTPRetryOverride); err != nil {
					return fmt.Errorf("provider %q: %w", entry.ID, err)
				}
				entry.HTTPRetry = entry.HTTPRetryOverride.Clone()
			}
			continue
		}
		if entry.HTTPRetryOverride != nil {
			return fmt.Errorf("provider %q declares both http_retry_profile and http_retry", entry.ID)
		}
		profile, ok := cfg.HTTPRetryProfiles[profileName]
		if !ok {
			return fmt.Errorf("provider %q references unknown http_retry_profile %q", entry.ID, profileName)
		}
		entry.HTTPRetryProfile = profileName
		entry.HTTPRetry = profile.HTTPRetry.Clone()
	}
	return nil
}

func validateProviderHTTPRetryProfile(name string, profile providerHTTPRetryProfile) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("http_retry_profiles contains an empty name")
	}
	switch profile.ServiceClass {
	case providerServiceClassHosted, providerServiceClassLocal, providerServiceClassSDK:
	default:
		return fmt.Errorf("http_retry_profile %q has unsupported service_class %q", name, profile.ServiceClass)
	}
	if err := providerretry.ValidateHTTPRetry(profile.HTTPRetry); err != nil {
		return fmt.Errorf("http_retry_profile %q: %w", name, err)
	}
	if profile.ServiceClass != providerServiceClassHosted {
		return nil
	}
	if profile.HTTPRetry.MaxRetries < 1 {
		return fmt.Errorf("hosted http_retry_profile %q must retry transient statuses", name)
	}
	for _, status := range []int{
		http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusGatewayTimeout,
	} {
		if !slices.Contains(profile.HTTPRetry.Statuses, status) {
			return fmt.Errorf("hosted http_retry_profile %q must retry HTTP %d", name, status)
		}
	}
	if profile.HTTPRetry.Capacity == nil ||
		profile.HTTPRetry.Capacity.MaxRetries < 1 ||
		!slices.Contains(profile.HTTPRetry.Capacity.Statuses, http.StatusServiceUnavailable) {
		return fmt.Errorf("hosted http_retry_profile %q must have a capacity retry for HTTP 503", name)
	}
	return nil
}
