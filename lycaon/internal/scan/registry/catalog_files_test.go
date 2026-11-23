package registry_test

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/configlayout"
	scancatalog "github.com/lycaon/lycaon/internal/scan/catalog"
	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	"github.com/lycaon/lycaon/internal/scan/registry"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestRegistryLoadsMissingBinaryPolicyFromDeviceCatalog(t *testing.T) {
	for _, skip := range []bool{true, false} {
		name := "required"
		if skip {
			name = "optional"
		}
		t.Run(name, func(t *testing.T) {
			root, home := configlayout.FindModuleRoot(), t.TempDir()
			enabled := true
			entry := scancatalog.ScannerEntry{
				ID: "missing", Driver: scancatalog.DriverExternal, Engine: "missing",
				ScopeKind: string(scancatalog.ScopeSourceDriver), Categories: []string{"sast", "security"},
				Command:      []string{"definitely-not-a-scanner-binary-xyz", scancatalog.ArgTokenScanTarget},
				OutputParser: scanoutput.OutputParserSARIF, Enabled: &enabled, SkipIfBinaryMissing: &skip,
			}
			catalog := scancatalog.NewCatalogStore(root, home, nil)
			testutil.FailErr(t, "save device scanner", catalog.SaveDeviceScannerEntry(t.Context(), entry))
			reg, err := registry.New(t.Context(), registry.Options{ModuleRoot: root, HomeDir: home})
			if !skip {
				if err == nil {
					_ = reg.Close(t.Context())
					t.Fatal("required missing binary did not fail registry construction")
				}
				return
			}
			testutil.FailErr(t, "load registry", err)
			t.Cleanup(func() { testutil.FailErr(t, "close registry", reg.Close(context.WithoutCancel(t.Context()))) })
			if _, err := reg.Get(entry.ID); err == nil {
				t.Fatal("missing scanner was registered")
			}
			if got := reg.List(api.ScanCategorySAST); len(got) != 0 {
				t.Fatalf("SAST selection = %+v, want optional missing scanner omitted", got)
			}
		})
	}
}
