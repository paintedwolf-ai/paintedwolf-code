package agentdef

import (
	"runtime"
	"testing"
	"weak"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/testutil"
)

func effectiveProfilesCached(key weak.Pointer[extpacks.EffectiveCatalog]) bool {
	effectiveCacheMu.RLock()
	defer effectiveCacheMu.RUnlock()
	_, ok := effectiveCache[key]
	return ok
}

// loadThenDropEffective loads through the cache and returns only a weak key, so the catalog is unreachable afterwards.
func loadThenDropEffective(t *testing.T) weak.Pointer[extpacks.EffectiveCatalog] {
	t.Helper()
	content, err := extpacks.DiscoverStockContent()
	testutil.FailErr(t, "DiscoverStockContent failed", err)
	catalog := extpacks.Resolve(t.Context(), extpacks.ResolveInput{Packs: content, Desired: extpacks.EmptyDesired()})

	first, err := LoadEffectiveWithCatalog(catalog)
	testutil.FailErr(t, "first LoadEffectiveWithCatalog failed", err)
	if len(first) == 0 {
		t.Fatal("stock catalog loaded no agent profiles")
	}
	first[0] = Profile{}
	second, err := LoadEffectiveWithCatalog(catalog)
	testutil.FailErr(t, "second LoadEffectiveWithCatalog failed", err)
	if second[0].ID == "" {
		t.Fatal("overwriting a returned profile changed the cached profile list")
	}

	key := weak.Make(catalog)
	if !effectiveProfilesCached(key) {
		t.Fatal("loaded catalog has no cache entry")
	}
	return key
}

func TestEffectiveProfilesCacheReleasesCollectedCatalogs(t *testing.T) {
	key := loadThenDropEffective(t)
	for i := 0; i < 100 && effectiveProfilesCached(key); i++ {
		runtime.GC()
		runtime.Gosched()
	}
	if effectiveProfilesCached(key) {
		t.Fatal("cache entry outlived its collected catalog")
	}
}
