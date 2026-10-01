package webresearch

import (
	"testing"
)

// The declared set is derived from the catalog, so a real fan-out declares the
// endpoints it will actually dial — including the direct pipeline's seed providers,
// which are catalog policy rather than a user preference.
func TestDeclaredEndpointHostsCoversSeedAndResultsProviders(t *testing.T) {
	t.Parallel()
	reg := testRegistry(t)
	settings := Settings{
		SearchEnabled:    true,
		Config:           map[string]map[string]string{},
		EnabledProviders: []string{directWireProviderID},
	}
	cat := reg.Catalog()
	for _, entry := range cat.Entries() {
		settings.Config[entry.ID] = resolveProviderConfig(entry, nil)
	}
	expandDirectBundledResults(&settings, cat)

	hosts := DeclaredEndpointHosts(settings, reg)
	if len(hosts) == 0 {
		t.Fatal("a default fan-out must declare the endpoints it will dial")
	}

	// Every probed seed provider must appear in the declared set.
	for _, id := range resolveSeedProviders(settings, reg) {
		want := endpointHost(keylessEndpoint(settings, id))
		if want == "" {
			continue
		}
		if !containsHost(hosts, want) {
			t.Fatalf("seed provider %s dials %s but it is not declared", id, want)
		}
	}

	// Normalized: sorted, lower-case, no duplicates.
	for i, h := range hosts {
		if h == "" {
			t.Fatal("empty host in declared set")
		}
		if i > 0 && hosts[i-1] >= h {
			t.Fatalf("declared set must be sorted and deduped: %v", hosts)
		}
	}
}

// An unconfigured provider is not in the fan-out, so declaring its endpoint would
// claim a destination this call cannot reach.
func TestDeclaredEndpointHostsSkipsUnconfiguredProviders(t *testing.T) {
	t.Parallel()
	reg := testRegistry(t)
	empty := Settings{SearchEnabled: true, Config: map[string]map[string]string{}}
	if hosts := DeclaredEndpointHosts(empty, reg); len(hosts) != 0 {
		t.Fatalf("no configured provider must declare no destinations, got %v", hosts)
	}
	if hosts := DeclaredEndpointHosts(empty, nil); hosts != nil {
		t.Fatalf("no registry must declare nothing, got %v", hosts)
	}
}

func TestEndpointHostExtractsDialedHost(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"https://crates.io":               "crates.io",
		"https://api.example.com:8443/v1": "api.example.com",
		"":                                "",
		"://nonsense":                     "",
	}
	for endpoint, want := range cases {
		if got := endpointHost(endpoint); got != want {
			t.Fatalf("endpointHost(%q) = %q want %q", endpoint, got, want)
		}
	}
}

func containsHost(hosts []string, want string) bool {
	for _, h := range hosts {
		if h == want {
			return true
		}
	}
	return false
}
