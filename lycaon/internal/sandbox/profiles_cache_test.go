package sandbox

import (
	"runtime"
	"testing"
	"weak"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/testutil"
)

func toolProfilesCached(key weak.Pointer[extpacks.EffectiveCatalog]) bool {
	toolProfilesCacheMu.RLock()
	defer toolProfilesCacheMu.RUnlock()
	_, ok := toolProfilesCache[key]
	return ok
}

// loadThenDropToolProfiles loads through the cache and returns only a weak key, so the catalog is unreachable afterwards.
func loadThenDropToolProfiles(t *testing.T) weak.Pointer[extpacks.EffectiveCatalog] {
	t.Helper()
	content, err := extpacks.DiscoverStockContent()
	testutil.FailErr(t, "DiscoverStockContent failed", err)
	catalog := extpacks.Resolve(t.Context(), extpacks.ResolveInput{Packs: content, Desired: extpacks.EmptyDesired()})

	first, err := LoadToolProfilesWithCatalog(catalog)
	testutil.FailErr(t, "first LoadToolProfilesWithCatalog failed", err)
	if len(first) == 0 {
		t.Fatal("stock catalog loaded no tool profiles")
	}
	for name := range first[0].Tools {
		delete(first[0].Tools, name)
	}
	second, err := LoadToolProfilesWithCatalog(catalog)
	testutil.FailErr(t, "second LoadToolProfilesWithCatalog failed", err)
	if len(second[0].Tools) == 0 {
		t.Fatalf("mutating a returned profile changed the cached profile %q", second[0].ID)
	}

	key := weak.Make(catalog)
	if !toolProfilesCached(key) {
		t.Fatal("loaded catalog has no cache entry")
	}
	return key
}

func TestToolProfilesCacheReturnsClonesAndReleasesCollectedCatalogs(t *testing.T) {
	key := loadThenDropToolProfiles(t)
	for i := 0; i < 100 && toolProfilesCached(key); i++ {
		runtime.GC()
		runtime.Gosched()
	}
	if toolProfilesCached(key) {
		t.Fatal("cache entry outlived its collected catalog")
	}
}
