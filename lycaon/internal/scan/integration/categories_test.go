package integration

import (
	"testing"

	"github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestResolveScanCategoriesAll(t *testing.T) {
	got, err := scan.ResolveScanCategories([]api.ScanCategory{"all"})
	if err != nil {
		t.Fatalf("ResolveScanCategories: %v", err)
	}
	if len(got) != len(scan.PackScanCategories()) {
		t.Fatalf("got %v", got)
	}
	seen := make(map[api.ScanCategory]bool, len(got))
	for _, c := range got {
		if !api.IsKnownScanCategory(c) {
			t.Fatalf("unknown category %q", c)
		}
		seen[c] = true
	}
	for _, c := range scan.PackScanCategories() {
		if !seen[c] {
			t.Fatalf("missing %q in %v", c, got)
		}
	}
}

func TestResolveScanCategoriesInvalid(t *testing.T) {
	_, err := scan.ResolveScanCategories([]api.ScanCategory{"nope"})
	if err == nil {
		t.Fatal("expected error for invalid category")
	}
}

func TestResolveScanCategoriesDefaultEmpty(t *testing.T) {
	got, err := scan.ResolveScanCategories(nil)
	if err != nil {
		t.Fatalf("ResolveScanCategories: %v", err)
	}
	if len(got) != 2 || got[0] != api.ScanCategorySecurity || got[1] != api.ScanCategorySecret {
		t.Fatalf("default = %v", got)
	}
}

func TestParseScanCategoryArgsAll(t *testing.T) {
	got, err := scan.ParseScanCategoryArgs([]any{"all"})
	if err != nil {
		t.Fatalf("ParseScanCategoryArgs: %v", err)
	}
	if len(got) != len(scan.PackScanCategories()) {
		t.Fatalf("got %v", got)
	}
}
