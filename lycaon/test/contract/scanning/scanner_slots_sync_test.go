package contract

import (
	"strings"
	"testing"

	scancatalog "github.com/lycaon/lycaon/internal/scan/catalog"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// Every slot needs a label and a hint, or a job renders with a bare id.
func TestScannerSlotCopyCoversEverySlot(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	copySrc := contractcheck.ReadRepoFile(t, root, "lycaon-den/src/settings/extensions/scanners-settings-copy.ts")
	for _, slot := range scancatalog.SlotCategories() {
		if !strings.Contains(copySrc, slot+": \"") {
			t.Errorf("scanners copy has no label/hint entry for slot %q", slot)
		}
	}
}

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
