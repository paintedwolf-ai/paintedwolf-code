package integration

import (
	"github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/internal/scan/scantest"
	"testing"

	scantoolapi "github.com/lycaon/lycaon/internal/scan/toolapi"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestRegistryPackCategoriesFromEngines(t *testing.T) {
	reg := &fanOutMockRegistry{scanners: []scan.CodeScanner{
		&scantest.MockScanner{IDVal: "lycaon-sca", CategoryList: []api.ScanCategory{api.ScanCategorySCA}},
		&scantest.MockScanner{IDVal: "lycaon-secrets", CategoryList: []api.ScanCategory{api.ScanCategorySecret}},
	}}
	got := scantoolapi.RegistryPackCategories(reg)
	want := map[string]bool{"sca": true, "secret": true}
	if len(got) != len(want) {
		t.Fatalf("categories = %v, want %v", got, want)
	}
	for _, c := range got {
		if !want[c] {
			t.Fatalf("unexpected category %q", c)
		}
	}
}
