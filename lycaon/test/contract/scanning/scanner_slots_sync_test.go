package contract

import (
	"regexp"
	"strings"
	"testing"

	scancatalog "github.com/lycaon/lycaon/internal/scan/catalog"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

var scannerSlotsTSPattern = regexp.MustCompile(
	`export const SCANNER_SLOTS = \[([^\]]*)\] as const;`,
)

// The Den hardcodes the scanner slot ids because slots must render in a fixed
// order even when a slot is empty, so they cannot be derived from the catalog
// response. That makes this the one scanner list that can drift from Go —
// everything else (the known-scanner catalog) is served over the wire.
func TestScannerSlotsMatchGo(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	src := contractcheck.ReadRepoFile(t, root, "lycaon-den/src/settings/extensions/scanners-catalog-model.ts")

	match := scannerSlotsTSPattern.FindStringSubmatch(src)
	if match == nil {
		t.Fatal("SCANNER_SLOTS not found in scanners-catalog-model.ts")
	}
	var tsSlots []string
	for _, part := range strings.Split(match[1], ",") {
		part = strings.TrimSpace(strings.Trim(strings.TrimSpace(part), `"`))
		if part != "" {
			tsSlots = append(tsSlots, part)
		}
	}

	goSlots := scancatalog.SlotCategories()
	if len(tsSlots) != len(goSlots) {
		t.Fatalf("Den SCANNER_SLOTS %v != Go SlotCategories %v", tsSlots, goSlots)
	}
	for i, want := range goSlots {
		if tsSlots[i] != want {
			t.Fatalf("slot %d: Den %q != Go %q (order is display order and must match)",
				i, tsSlots[i], want)
		}
	}
}

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
