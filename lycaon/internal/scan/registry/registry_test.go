package registry_test

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/scan"
	scancatalog "github.com/lycaon/lycaon/internal/scan/catalog"
	"github.com/lycaon/lycaon/internal/scan/drivers/libraryworker"
	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	"github.com/lycaon/lycaon/internal/scan/registry"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestRunBestSelectsScalibrForSCA(t *testing.T) {
	root := configlayout.FindModuleRoot()
	reg, err := registry.New(registry.Options{ModuleRoot: root})
	testutil.FailErr(t, "registry.New failed", err)
	candidates := reg.List(api.ScanCategorySCA)
	if len(candidates) == 0 {
		t.Fatal("expected sca scanner")
	}
	if candidates[0].ID != "lycaon-sca" {
		t.Fatalf("sca scanner = %q", candidates[0].ID)
	}
}

func TestLibraryScannerUsesManagedWorker(t *testing.T) {
	reg, err := registry.New(registry.Options{ModuleRoot: configlayout.FindModuleRoot()})
	testutil.FailErr(t, "registry.New", err)
	scanner, err := reg.Get("lycaon-sca")
	testutil.FailErr(t, "get SCA scanner", err)
	if _, ok := scanner.(*libraryworker.Scanner); !ok {
		t.Fatalf("SCA scanner type = %T want managed library worker", scanner)
	}
}

func TestRunBestSelectsOpenGrepForSAST(t *testing.T) {
	root := configlayout.FindModuleRoot()
	reg, err := registry.New(registry.Options{ModuleRoot: root})
	testutil.FailErr(t, "registry.New failed", err)
	candidates := reg.List(api.ScanCategorySAST)
	found := false
	for _, c := range candidates {
		if c.ID == "lycaon-sast" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected lycaon-sast in sast list")
	}
}

func TestRunBestRequiresProjectDir(t *testing.T) {
	root := configlayout.FindModuleRoot()
	reg, err := registry.New(registry.Options{ModuleRoot: root})
	testutil.FailErr(t, "registry.New failed", err)
	_, err = reg.RunBest(context.Background(), []api.ScanCategory{api.ScanCategorySecret}, scan.ScanRequest{})
	if err == nil {
		t.Fatal("expected project dir error")
	}
}

func TestReloadMakesDeviceSlotReplacementImmediatelyRunnable(t *testing.T) {
	root := configlayout.FindModuleRoot()
	home := t.TempDir()
	reg, err := registry.New(registry.Options{ModuleRoot: root, HomeDir: home})
	testutil.FailErr(t, "registry.New", err)
	store := scancatalog.NewCatalogStore(root, home, nil)

	enabled := true
	err = store.SaveDeviceScannerEntry(t.Context(), scancatalog.ScannerEntry{
		ID: "custom-sca", Driver: scancatalog.DriverExternal, Engine: "true", ScopeKind: string(scancatalog.ScopeDependencies), Categories: []string{"sca"},
		Command: []string{"true", scancatalog.ArgTokenScanTarget}, OutputParser: scanoutput.OutputParserSARIF, Enabled: &enabled,
	})
	testutil.FailErr(t, "SaveDeviceScannerEntry", err)
	testutil.FailErr(t, "Reload", reg.Reload())

	candidates := reg.List(api.ScanCategorySCA)
	if len(candidates) != 1 || candidates[0].ID != "custom-sca" {
		t.Fatalf("SCA selection = %+v, want custom-sca", candidates)
	}
	if _, err := reg.Get("custom-sca"); err != nil {
		t.Fatalf("custom-sca adapter missing after reload: %v", err)
	}
}

func TestListForProjectAppliesSlotOverrideWithoutChangingDeviceSelection(t *testing.T) {
	root := configlayout.FindModuleRoot()
	home := t.TempDir()
	projectDir := t.TempDir()
	store := scancatalog.NewCatalogStore(root, home, nil)
	disabled := false
	err := store.CreateUserExternalScanner(t.Context(), scancatalog.ScannerEntry{
		ID: "custom-sca", Engine: "true", ScopeKind: string(scancatalog.ScopeDependencies), Categories: []string{"sca"}, Command: []string{"true", scancatalog.ArgTokenScanTarget},
		OutputParser: scanoutput.OutputParserSARIF, Enabled: &disabled,
	})
	testutil.FailErr(t, "CreateUserExternalScanner", err)
	err = store.SetProjectScannerEnabled(t.Context(), projectDir, "custom-sca", true)
	testutil.FailErr(t, "SetProjectScannerEnabled", err)

	reg, err := registry.New(registry.Options{
		ModuleRoot: root,
		HomeDir:    home,
		ProjectTierApplies: func(context.Context, string) bool {
			return true
		},
	})
	testutil.FailErr(t, "registry.New", err)

	device := reg.List(api.ScanCategorySCA)
	if len(device) != 1 || device[0].ID != "lycaon-sca" {
		t.Fatalf("device SCA selection = %+v, want lycaon-sca", device)
	}
	projectSelection := reg.ListForProject(t.Context(), projectDir, api.ScanCategorySCA)
	if len(projectSelection) != 1 || projectSelection[0].ID != "custom-sca" {
		t.Fatalf("project SCA selection = %+v, want custom-sca", projectSelection)
	}
}
