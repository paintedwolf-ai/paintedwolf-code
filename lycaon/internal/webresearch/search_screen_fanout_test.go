package webresearch

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/secretmatch"
)

func searchTestProvider(t *testing.T, ctx context.Context, provider SearchProvider, settings Settings, query string, limit int) WebSearchResult {
	t.Helper()
	reg := NewRegistry(testCatalog(t))
	reg.Register(provider)
	return Search(ctx, SearchOptions{Registry: reg, Settings: settings, Provider: provider.ID(), Query: query, Limit: limit, SkipResultCache: true})
}

type queryRecorder struct {
	id      string
	mu      sync.Mutex
	queries []string
}

func (p *queryRecorder) ID() string             { return p.id }
func (*queryRecorder) Kind() ProviderKind       { return KindKeyedExtra }
func (*queryRecorder) Configured(Settings) bool { return true }
func (p *queryRecorder) Search(_ context.Context, _ Settings, query string, _ int) providerOutcome {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.queries = append(p.queries, query)
	return providerOutcome{providerID: p.id, ok: true}
}

func TestEverySearchProviderReceivesScreenedQuery(t *testing.T) {
	for _, decision := range []secretmatch.Decision{secretmatch.Withhold, secretmatch.SendRedacted, secretmatch.SendUnchanged} {
		t.Run(string(decision), func(t *testing.T) {
			registered := testRegistry(t)
			reg := NewRegistry(registered.Catalog())
			var providers []*queryRecorder
			for _, id := range registered.IDs() {
				p := &queryRecorder{id: id}
				reg.Register(p)
				providers = append(providers, p)
			}
			if len(providers) == 0 || !reg.Has("ietf_rfc") {
				t.Fatal("catalog fixture must include IETF")
			}
			settings := Settings{EnabledProviders: reg.IDs()}
			asks := 0
			destination := webSearchDestination(settings, reg)
			ctx := withSecretScreen(t.Context(), testSecretMatcher(t), func(_ context.Context, a secretmatch.Alert) (secretmatch.Resolution, error) {
				asks++
				if a.DestinationID != destination.id {
					t.Errorf("destination = %q", a.DestinationID)
				}
				return secretmatch.Resolution{Decision: decision}, nil
			}, "", destination)
			result := Search(ctx, SearchOptions{Query: "docs " + plantedAWS, Settings: settings, Registry: reg, SkipResultCache: true})
			if asks != 1 {
				t.Fatalf("asks = %d", asks)
			}
			if result.OK == (decision == secretmatch.Withhold) {
				t.Fatalf("result = %+v", result)
			}
			for _, p := range providers {
				p.mu.Lock()
				queries := append([]string(nil), p.queries...)
				p.mu.Unlock()
				if decision == secretmatch.Withhold {
					if len(queries) != 0 {
						t.Errorf("%s ran on refusal", p.id)
					}
					continue
				}
				if len(queries) != 1 {
					t.Errorf("%s queries = %v", p.id, queries)
					continue
				}
				if decision == secretmatch.SendRedacted && (strings.Contains(queries[0], plantedAWS) || !strings.Contains(queries[0], "[REDACTED]")) {
					t.Errorf("%s query = %q", p.id, queries[0])
				}
				if decision == secretmatch.SendUnchanged && !strings.Contains(queries[0], plantedAWS) {
					t.Errorf("%s query = %q", p.id, queries[0])
				}
			}
		})
	}
}
