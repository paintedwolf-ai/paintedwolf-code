package llm

import (
	"sort"
	"time"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
)

// providerObservation records the provider accepting its credentials on a live call.
type providerObservation struct {
	AcceptedAt time.Time
}

type providerReadiness struct {
	Configuration  string
	Authentication string
}

// providerRegistrySnapshot is the immutable resolution boundary read by live
// requests. Discovery and Settings mutations construct a complete replacement
// and publish it with one atomic pointer swap; readers never observe a provider
// while its model catalog is being rewritten.
type providerRegistrySnapshot struct {
	providers        map[string]modelcall.Provider
	entries          map[string]CatalogEntry
	credentialValues map[string]string
	credentialStored map[string]bool
	ids              []string
	defaultID        string
	configured       map[string]bool
	readiness        map[string]providerReadiness
	observations     map[string]providerObservation
}

type providerRegistrySnapshotInput struct {
	Providers        map[string]modelcall.Provider
	Entries          map[string]CatalogEntry
	CredentialValues map[string]string
	CredentialStored map[string]bool
	DefaultID        string
	Configured       map[string]bool
	Readiness        map[string]providerReadiness
	Observations     map[string]providerObservation
}

func newProviderRegistrySnapshot(input providerRegistrySnapshotInput) *providerRegistrySnapshot {
	providers := make(map[string]modelcall.Provider, len(input.Providers))
	ids := make([]string, 0, len(input.Providers))
	for id, provider := range input.Providers {
		providers[id] = provider
		ids = append(ids, id)
	}
	sort.Strings(ids)
	entries := make(map[string]CatalogEntry, len(input.Entries))
	for id, entry := range input.Entries {
		if _, exists := providers[id]; exists {
			entries[id] = cloneCatalogEntry(entry)
		}
	}
	credentialStored := make(map[string]bool, len(input.CredentialStored))
	for id, stored := range input.CredentialStored {
		if _, exists := providers[id]; exists {
			credentialStored[id] = stored
		}
	}
	credentialValues := make(map[string]string, len(input.CredentialValues))
	for id, value := range input.CredentialValues {
		if _, exists := providers[id]; exists {
			credentialValues[id] = value
		}
	}
	configured := make(map[string]bool, len(input.Configured))
	for id, ready := range input.Configured {
		if _, exists := providers[id]; exists {
			configured[id] = ready
		}
	}
	observations := make(map[string]providerObservation)
	for id, observation := range input.Observations {
		if _, exists := providers[id]; exists {
			observations[id] = observation
		}
	}
	readiness := make(map[string]providerReadiness, len(input.Readiness))
	for id, state := range input.Readiness {
		if _, exists := providers[id]; exists {
			readiness[id] = state
		}
	}
	return &providerRegistrySnapshot{
		providers: providers, entries: entries, credentialValues: credentialValues,
		credentialStored: credentialStored,
		ids:              ids, defaultID: input.DefaultID,
		configured: configured, readiness: readiness, observations: observations,
	}
}

func (s *providerRegistrySnapshot) cloneCredentialValues() map[string]string {
	out := make(map[string]string, len(s.credentialValues))
	for id, value := range s.credentialValues {
		out[id] = value
	}
	return out
}

func (s *providerRegistrySnapshot) cloneEntries() map[string]CatalogEntry {
	out := make(map[string]CatalogEntry, len(s.entries))
	for id, entry := range s.entries {
		out[id] = cloneCatalogEntry(entry)
	}
	return out
}

func (s *providerRegistrySnapshot) cloneCredentialStored() map[string]bool {
	out := make(map[string]bool, len(s.credentialStored))
	for id, stored := range s.credentialStored {
		out[id] = stored
	}
	return out
}

func (s *providerRegistrySnapshot) cloneReadiness() map[string]providerReadiness {
	out := make(map[string]providerReadiness, len(s.readiness))
	for id, state := range s.readiness {
		out[id] = state
	}
	return out
}

func (s *providerRegistrySnapshot) cloneConfigured() map[string]bool {
	out := make(map[string]bool, len(s.configured))
	for id, ready := range s.configured {
		out[id] = ready
	}
	return out
}

func (s *providerRegistrySnapshot) cloneObservations() map[string]providerObservation {
	out := make(map[string]providerObservation, len(s.observations))
	for id, observation := range s.observations {
		out[id] = observation
	}
	return out
}

func (s *providerRegistrySnapshot) cloneProviders() map[string]modelcall.Provider {
	out := make(map[string]modelcall.Provider, len(s.providers))
	for id, provider := range s.providers {
		out[id] = provider
	}
	return out
}

func newEmptyRegistry() *Registry {
	r := &Registry{}
	r.snapshot.Store(newProviderRegistrySnapshot(providerRegistrySnapshotInput{}))
	return r
}
