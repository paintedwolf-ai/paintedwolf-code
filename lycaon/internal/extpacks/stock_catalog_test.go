package extpacks_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestResolveStockCatalogUsesBundledPacksOnly(t *testing.T) {
	t.Parallel()
	eff, err := extpacks.ResolveStockCatalog(t.Context(), nil)
	testutil.FailErr(t, "resolve stock catalog", err)
	platformLoaded := false
	for _, pack := range eff.Packs {
		if pack.Kind != extpacks.PackKindStock && pack.Contributing {
			t.Fatalf("non-stock pack %q contributes to the stock catalog", pack.ID)
		}
		if pack.ID == "painted-wolf/platform" && pack.Contributing {
			platformLoaded = true
		}
	}
	if !platformLoaded {
		t.Fatal("painted-wolf/platform must contribute to the stock catalog")
	}
	if len(eff.Desired.Packs) != 0 || len(eff.Desired.Disabled) != 0 {
		t.Fatalf("stock desired state = %+v, want empty", eff.Desired)
	}
}

func TestSecurityPackVersionResolvesWithPlatformDependency(t *testing.T) {
	eff, err := extpacks.ResolveStockCatalog(t.Context(), nil)
	testutil.FailErr(t, "resolve catalog", err)
	for _, pack := range eff.Packs {
		if pack.ID == "painted-wolf/security-survey" {
			if pack.Version != "2.0.0" || !pack.Contributing {
				t.Fatalf("security pack = %+v", pack)
			}
			return
		}
	}
	t.Fatal("security pack is missing")
}
