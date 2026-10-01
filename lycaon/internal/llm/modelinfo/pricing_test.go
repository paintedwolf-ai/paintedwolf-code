package modelinfo

import (
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/pricing"
)

func TestDiscoveredRateFromModels(t *testing.T) {
	models := []Entry{
		{ID: "catalog", InputPer1K: 0.01, OutputPer1K: 0.02, Currency: "USD", PriceProvenance: PriceProvenanceCatalog},
		{ID: "live", DiscoveredPricing: &pricing.Rate{InputPer1K: new(float64(0.003)), OutputPer1K: new(float64(0.015)), Currency: "USD"}, InputPer1K: 0.003, OutputPer1K: 0.015, Currency: "USD", PriceProvenance: PriceProvenanceDiscovered},
		{ID: "empty-live", PriceProvenance: PriceProvenanceDiscovered},
	}
	if _, ok := DiscoveredRateFromEntries(models, "catalog"); ok {
		t.Fatal("catalog provenance must not yield a discovered rate")
	}
	rate, ok := DiscoveredRateFromEntries(models, "live")
	if !ok {
		t.Fatal("expected discovered rate")
	}
	want := pricing.Rate{InputPer1K: new(float64(0.003)), OutputPer1K: new(float64(0.015)), Currency: "USD"}
	if !reflect.DeepEqual(rate, want) {
		t.Fatalf("rate = %+v want %+v", rate, want)
	}
	if _, ok := DiscoveredRateFromEntries(models, "empty-live"); ok {
		t.Fatal("missing rates must not count as discovered")
	}
}
