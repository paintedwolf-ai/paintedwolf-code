package webresearch

import (
	"context"
	"os"
	"testing"
)

// TestVerticalProvidersLive runs one real query per keyless vertical provider.
//
//	LYCAON_LIVE_KEYLESS_VERTICALS=1 ./task test:digest -- ./internal/webresearch \
//	  -run VerticalProvidersLive -count=1 -timeout=5m
func TestVerticalProvidersLive(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("live provider calls")
	}
	if os.Getenv("LYCAON_LIVE_KEYLESS_VERTICALS") == "" {
		t.Skip("set LYCAON_LIVE_KEYLESS_VERTICALS=1 to run")
	}
	reg := testRegistry(t)
	s := DefaultSettings(nil, nil, reg.Catalog())
	// Exercise every configured keyless provider.
	// Shared pacing keys serialize parallel subtests.
	for _, entry := range reg.Catalog().Entries() {
		if entry.Kind != KindKeyless {
			continue
		}
		id := entry.ID
		t.Run(id, func(t *testing.T) {
			t.Parallel()
			runLiveVerticalSearch(t, reg, s, id)
		})
	}
}

func runLiveVerticalSearch(t *testing.T, reg *Registry, s Settings, id string) {
	t.Helper()
	p := reg.Get(id)
	if p == nil {
		t.Fatalf("missing provider %q", id)
	}
	query := "test"
	if entry, ok := reg.Catalog().Entry(id); ok && entry.TestQuery != "" {
		query = entry.TestQuery
	}
	out := p.Search(context.Background(), s, query, 3)
	if !out.ok {
		t.Fatalf("search failed: reason=%s detail=%s", out.reason, out.detail)
	}
	if len(out.hits) == 0 {
		t.Fatal("expected at least one hit")
	}
}
