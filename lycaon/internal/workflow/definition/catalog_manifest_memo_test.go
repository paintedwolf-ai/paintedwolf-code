package definition

import (
	"testing"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestLoadManifestsFromCatalogReturnsIndependentMaps(t *testing.T) {
	catalog, err := extpacks.CatalogForConsumers()
	testutil.FailErr(t, "catalog for consumers", err)

	first, firstSources, err := loadManifestsFromCatalog(catalog)
	testutil.FailErr(t, "first load", err)
	if len(first) == 0 {
		t.Fatal("expected catalog manifests")
	}
	want := len(first)

	first["injected-project-workflow"] = Manifest{ID: "injected-project-workflow"}
	for id := range firstSources {
		delete(firstSources, id)
		break
	}

	second, secondSources, err := loadManifestsFromCatalog(catalog)
	testutil.FailErr(t, "second load", err)
	if len(second) != want {
		t.Fatalf("second load has %d manifests, want %d — the first caller's overlay leaked into the cache", len(second), want)
	}
	if _, leaked := second["injected-project-workflow"]; leaked {
		t.Fatal("project workflow injected by the first caller leaked into a later load")
	}
	if len(secondSources) != len(firstSources)+1 {
		t.Fatalf("sources map is shared: second=%d first=%d", len(secondSources), len(firstSources))
	}
}

func TestLoadManifestsFromCatalogHonorsCatalogFiltering(t *testing.T) {
	catalog, err := extpacks.CatalogForConsumers()
	testutil.FailErr(t, "catalog for consumers", err)
	full, _, err := loadManifestsFromCatalog(catalog)
	testutil.FailErr(t, "load with active catalog", err)

	empty := &extpacks.EffectiveCatalog{
		Units:    map[string]extpacks.UnitEffective{},
		Loaded:   map[string]extpacks.UnitEffective{},
		Revision: "test-empty-catalog",
	}
	filtered, _, err := loadManifestsFromCatalog(empty)
	testutil.FailErr(t, "load with empty catalog", err)

	if len(filtered) >= len(full) {
		t.Fatalf("empty catalog kept %d manifests vs %d for the active catalog — filtering is being served from the wrong cache entry", len(filtered), len(full))
	}
}
