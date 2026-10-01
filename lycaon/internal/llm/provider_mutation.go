package llm

import (
	"fmt"
	"strings"
)

type ProviderInUseError struct {
	ProviderID string
	Scopes     []string
}

type ProviderNotFoundError struct{ ProviderID string }

func (e *ProviderNotFoundError) Error() string {
	return fmt.Sprintf("provider %q not found", e.ProviderID)
}

func (e *ProviderInUseError) Error() string {
	return fmt.Sprintf("provider %q is assigned in %s", e.ProviderID, strings.Join(e.Scopes, ", "))
}

func policyReferencesProvider(policy ModelPolicy, providerID string) bool {
	if policy.Coordinator.ProviderID == providerID || policy.Lite.ProviderID == providerID {
		return true
	}
	for _, ref := range policy.AgentPool.Models {
		if ref.ProviderID == providerID {
			return true
		}
	}
	return false
}

// ProviderReferenceScopes returns known policy layers using an instance.
func (s *PolicyStore) ProviderReferenceScopes(providerID string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var scopes []string
	if policyReferencesProvider(s.bundled, providerID) {
		scopes = append(scopes, "bundled policy")
	}
	if s.global != nil && policyReferencesProvider(*s.global, providerID) {
		scopes = append(scopes, "global policy")
	}
	for projectDir, policy := range s.projectCache {
		if policyReferencesProvider(policy, providerID) {
			scopes = append(scopes, "project "+projectDir)
		}
	}
	return scopes
}
