package catalog

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

// CategorySecurity is a cross-slot tag.
const CategorySecurity = "security"

// SlotCategories returns slots in display order, each with one selected scanner.
func SlotCategories() []string {
	return []string{
		string(api.ScanCategorySAST),
		string(api.ScanCategorySCA),
		string(api.ScanCategorySecret),
	}
}

// PrimaryCategory returns the slot a scanner competes for: its first category
// that is not the generic "security" tag.
func PrimaryCategory(categories []string) string {
	for _, c := range categories {
		c = strings.TrimSpace(c)
		if c == "" || c == CategorySecurity {
			continue
		}
		return c
	}
	return ""
}

// IsSlotCategory reports whether category maps to a scanner slot.
func IsSlotCategory(category string) bool {
	for _, c := range SlotCategories() {
		if c == category {
			return true
		}
	}
	return false
}

// SelectScannerForCategory replaces the slot selection; an empty ID clears it.
func (s *CatalogStore) SelectScannerForCategory(ctx context.Context, category, scannerID string) error {
	category = strings.TrimSpace(category)
	if !IsSlotCategory(category) {
		return storeErr(RejectInvalidEntry, fmt.Sprintf("unknown scanner slot %q", category))
	}
	path := UserScannersPath(s.homeDir)
	return s.update(ctx, path, true, func() error {
		merged, _, err := LoadMergedScannerCatalog(s.moduleRoot, "", s.homeDir)
		if err != nil {
			return err
		}

		scannerID = strings.TrimSpace(scannerID)
		if scannerID != "" {
			chosen, ok := findScannerEntry(merged, scannerID)
			if !ok {
				return storeErr(RejectInvalidEntry, fmt.Sprintf("unknown scanner id %q", scannerID))
			}
			if got := PrimaryCategory(chosen.Categories); got != category {
				return storeErr(RejectInvalidEntry,
					fmt.Sprintf("scanner %q covers %q, not %q", scannerID, got, category))
			}
		}

		overlay, err := LoadUserScannerOverlay(path)
		if err != nil {
			return err
		}
		if err := selectScannerInOverlay(overlay, merged.Scanners, category, scannerID); err != nil {
			return err
		}
		return saveUserScannerOverlay(path, overlay)
	})
}

func selectScannerInOverlay(overlay *ScannerConfig, candidates []ScannerEntry, category, scannerID string) error {
	for _, entry := range candidates {
		if PrimaryCategory(entry.Categories) != category {
			continue
		}
		if err := setScannerEnabledInOverlay(overlay, entry.ID, entry.ID == scannerID, false); err != nil {
			return err
		}
	}
	return nil
}

func findScannerEntry(cfg *ScannerConfig, id string) (ScannerEntry, bool) {
	if cfg == nil {
		return ScannerEntry{}, false
	}
	for _, entry := range cfg.Scanners {
		if entry.ID == id {
			return entry, true
		}
	}
	return ScannerEntry{}, false
}
