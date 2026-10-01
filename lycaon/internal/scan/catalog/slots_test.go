package catalog

import (
	"strings"
	"testing"
)

func TestPrimaryCategorySkipsGenericSecurityTag(t *testing.T) {
	cases := []struct {
		name       string
		categories []string
		want       string
	}{
		{"sast first", []string{"sast", "security"}, "sast"},
		{"security first", []string{"security", "sca"}, "sca"},
		{"secret", []string{"secret", "security"}, "secret"},
		{"security only", []string{"security"}, ""},
		{"empty", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := PrimaryCategory(tc.categories); got != tc.want {
				t.Fatalf("PrimaryCategory(%v) = %q, want %q", tc.categories, got, tc.want)
			}
		})
	}
}

// Each scan job selects one scanner slot.
func TestBundledScannersHavePrimaryCategory(t *testing.T) {
	cfg, err := LoadScannerConfig()
	if err != nil {
		t.Fatalf("load bundled scanners: %v", err)
	}
	seen := make(map[string]string)
	for _, entry := range cfg.Scanners {
		primary := PrimaryCategory(entry.Categories)
		if primary == "" {
			t.Fatalf("bundled scanner %q has no primary category", entry.ID)
		}
		if !IsSlotCategory(primary) {
			t.Fatalf("bundled scanner %q primary category %q maps to no slot", entry.ID, primary)
		}
		if prev, dup := seen[primary]; dup {
			t.Fatalf("slot %q claimed by both %q and %q — defaults must be one per slot",
				primary, prev, entry.ID)
		}
		seen[primary] = entry.ID
	}
	// Each slot has one bundled default.
	if len(seen) != len(SlotCategories()) {
		t.Fatalf("bundled scanners fill %d slots, want %d", len(seen), len(SlotCategories()))
	}
}

func TestCatalogEntriesLandInKnownSlots(t *testing.T) {
	cat, err := LoadScannerCatalog()
	if err != nil {
		t.Fatalf("load catalog: %v", err)
	}
	for _, entry := range cat.Entries() {
		if PrimaryCategory(entry.Categories) == "" {
			t.Fatalf("catalog scanner %q has no primary category", entry.ID)
		}
	}
}

func TestValidateScannerConfigRejectsMultipleEnabledPerSlot(t *testing.T) {
	enabled := true
	cfg := &ScannerConfig{Scanners: []ScannerEntry{
		{ID: "first", Driver: DriverLibrary, Impl: "one", Engine: "one", ScopeKind: string(ScopeCustom), Categories: []string{"sast"}, Enabled: &enabled},
		{ID: "second", Driver: DriverLibrary, Impl: "two", Engine: "two", ScopeKind: string(ScopeCustom), Categories: []string{"sast"}, Enabled: &enabled},
	}}
	err := ValidateScannerConfig(cfg)
	if err == nil || !strings.Contains(err.Error(), "multiple enabled scanners") {
		t.Fatalf("ValidateScannerConfig() = %v, want slot exclusivity error", err)
	}
}

func TestValidateScannerConfigRequiresKnownSlot(t *testing.T) {
	cfg := &ScannerConfig{Scanners: []ScannerEntry{{
		ID: "floating", Driver: DriverLibrary, Impl: "one", Engine: "one", ScopeKind: string(ScopeCustom), Categories: []string{"security"},
	}}}
	err := ValidateScannerConfig(cfg)
	if err == nil || !strings.Contains(err.Error(), "maps to no scanner slot") {
		t.Fatalf("ValidateScannerConfig() = %v, want missing slot error", err)
	}
}
