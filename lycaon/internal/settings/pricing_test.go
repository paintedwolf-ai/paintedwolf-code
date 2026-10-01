package settings_test

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestPricingStoreDefaultOn(t *testing.T) {
	store := newTestPricingStore(t)
	eff := store.Effective()
	if !eff.CostTrackingEnabled {
		t.Fatal("expected cost tracking on by default")
	}
	if eff.CostTrackingSince != nil {
		t.Fatal("expected nil since until first off→on stamp")
	}
	if len(eff.Sources) != 3 {
		t.Fatalf("sources = %d want 3", len(eff.Sources))
	}
	for i, source := range eff.Sources {
		if source.Enabled != (i == 0) {
			t.Fatalf("source %s enabled = %v, want only the first selected", source.ID, source.Enabled)
		}
	}
}

func TestPricingStoreStampSince(t *testing.T) {
	store := newTestPricingStore(t)
	fixed := time.Date(2025, 7, 1, 12, 0, 0, 0, time.UTC)
	store.SetClock(func() time.Time { return fixed })

	err := store.PutGlobal(settings.PricingUserOverlay{
		CostTrackingEnabled: boolPtr(true),
		Sources: []settings.PricingSourcePref{
			{ID: "models-dev", Enabled: true},
			{ID: "litellm", Enabled: false},
			{ID: "ai-pricing-fyi", Enabled: false},
		},
	})
	testutil.FailErr(t, "PutGlobal on", err)
	eff := store.Effective()
	if !eff.CostTrackingEnabled || eff.CostTrackingSince == nil || !eff.CostTrackingSince.Equal(fixed) {
		t.Fatalf("eff = %+v", eff)
	}

	err = store.PutGlobal(settings.PricingUserOverlay{
		CostTrackingEnabled: boolPtr(false),
		Sources: []settings.PricingSourcePref{
			{ID: "models-dev", Enabled: true},
		},
	})
	testutil.FailErr(t, "PutGlobal off", err)
	eff = store.Effective()
	if eff.CostTrackingEnabled || eff.CostTrackingSince != nil {
		t.Fatalf("after disable: %+v", eff)
	}
}

func TestPricingStoreRejectNoSource(t *testing.T) {
	store := newTestPricingStore(t)
	err := store.PutGlobal(settings.PricingUserOverlay{
		CostTrackingEnabled: boolPtr(true),
		Sources: []settings.PricingSourcePref{
			{ID: "models-dev", Enabled: false},
		},
	})
	if !errors.Is(err, settings.ErrPricingNoSource) {
		t.Fatalf("want ErrPricingNoSource, got %v", err)
	}
}

func TestPricingStoreAllowsNoSourceWhenTrackingIsOff(t *testing.T) {
	store := newTestPricingStore(t)
	err := store.PutGlobal(settings.PricingUserOverlay{
		CostTrackingEnabled: boolPtr(false),
		Sources: []settings.PricingSourcePref{
			{ID: "models-dev", Enabled: false},
			{ID: "litellm", Enabled: false},
			{ID: "ai-pricing-fyi", Enabled: false},
		},
	})
	testutil.FailErr(t, "PutGlobal off without source", err)
	if eff := store.Effective(); eff.CostTrackingEnabled {
		t.Fatalf("effective = %+v, want tracking off", eff)
	}
}

func TestPricingStoreRejectsMultipleSelectedSources(t *testing.T) {
	store := newTestPricingStore(t)
	err := store.PutGlobal(settings.PricingUserOverlay{
		CostTrackingEnabled: boolPtr(true),
		Sources: []settings.PricingSourcePref{
			{ID: "models-dev", Enabled: true},
			{ID: "litellm", Enabled: true},
		},
	})
	if !errors.Is(err, settings.ErrPricingMultipleSources) {
		t.Fatalf("want ErrPricingMultipleSources, got %v", err)
	}
}

func newTestPricingStore(t *testing.T) *settings.PricingStore {
	t.Helper()
	global := filepath.Join(t.TempDir(), "pricing.yaml")
	store, err := settings.NewPricingStoreAt(global)
	testutil.FailErr(t, "NewPricingStoreAt", err)
	return store
}
