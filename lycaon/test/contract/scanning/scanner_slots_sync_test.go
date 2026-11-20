package contract

import (
	"testing"

	scancatalog "github.com/lycaon/lycaon/internal/scan/catalog"
)

// The bundled defaults fill every slot, so a fresh install has one scanner per
// job with nothing to configure.
func TestBundledScannersFillEverySlot(t *testing.T) {
	t.Parallel()
	cfg, err := scancatalog.LoadScannerConfig()
	if err != nil {
		t.Fatalf("load bundled scanners: %v", err)
	}
	filled := make(map[string]string, len(cfg.Scanners))
	for _, entry := range cfg.Scanners {
		filled[scancatalog.PrimaryCategory(entry.Categories)] = entry.ID
	}
	for _, slot := range scancatalog.SlotCategories() {
		if filled[slot] == "" {
			t.Errorf("no bundled default for slot %q", slot)
		}
	}
}
