package contract

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/scan"
	scancadence "github.com/lycaon/lycaon/internal/scan/cadence"
	scancfg "github.com/lycaon/lycaon/internal/scan/configuration"
	scanregistry "github.com/lycaon/lycaon/internal/scan/registry"
	scantoolapi "github.com/lycaon/lycaon/internal/scan/toolapi"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil/scantest"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolschema"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestScanPackToolMetaMatchesBundledRegistry(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	reg, err := scanregistry.New(t.Context(), scanregistry.Options{
		ModuleRoot: filepath.Join(root, "lycaon"),
	})
	contractcheck.FailErr(t, "scan registry", err)

	sqlDB := testdbfixture.Open(t, "store.db")

	schemas, err := toolschema.LoadSchemaDir(filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "tools", "schemas"))
	contractcheck.FailErr(t, "load scan tool schemas", err)
	toolReg, err := tools.NewCatalogRegistry(schemas)
	contractcheck.FailErr(t, "new catalog registry", err)
	coord := scantest.Coordinator(t, scan.NewSQLStore(sqlDB), nil)
	if err := scantoolapi.RegisterScanTools(toolReg, coord, reg, scancadence.New(scan.StoreFromCoordinator(coord), coord, reg, nil, scancfg.DefaultGatesConfig(), nil), nil, nil, nil); err != nil {
		contractcheck.FailErr(t, "RegisterScanTools", err)
	}
	meta, ok := toolReg.Meta("scan_pack")
	if !ok {
		t.Fatal("scan_pack not registered")
	}
	enum := schemaCategoryEnum(t, meta.ArgsSchema)
	if !enum[scan.CategoryAll] {
		t.Fatalf("scan_pack categories enum %v lacks %q", enum, scan.CategoryAll)
	}
	scanners := reg.List()
	if len(scanners) == 0 {
		t.Fatal("bundled scan registry lists no scanners")
	}
	for _, scanner := range scanners {
		for _, category := range scanner.Categories {
			if !enum[string(category)] {
				t.Errorf("scanner %s category %q is not in the scan_pack categories enum %v", scanner.ID, category, enum)
			}
		}
	}
	// Categories without a bundled scanner serve project-configured scanners;
	// each must still be one the host accepts.
	for category := range enum {
		if category != scan.CategoryAll && !api.IsKnownScanCategory(api.ScanCategory(category)) {
			t.Errorf("scan_pack offers category %q outside the scan category vocabulary", category)
		}
	}
}

func schemaCategoryEnum(t *testing.T, schema map[string]any) map[string]bool {
	t.Helper()
	props, _ := schema["properties"].(map[string]any)
	if props == nil {
		t.Fatal("schema missing properties")
	}
	cat, _ := props["categories"].(map[string]any)
	items, _ := cat["items"].(map[string]any)
	rawEnum, _ := items["enum"].([]any)
	out := make(map[string]bool, len(rawEnum))
	for _, v := range rawEnum {
		if s, ok := v.(string); ok {
			out[s] = true
		}
	}
	return out
}
