package catalog_test

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/configlayout"
	scancatalog "github.com/lycaon/lycaon/internal/scan/catalog"
	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
)

func userScanner(id, category string) scancatalog.ScannerEntry {
	return scancatalog.ScannerEntry{
		ID: id, Engine: id, ScopeKind: string(scancatalog.ScopeDependencies), Categories: []string{category},
		Command: []string{"true", scancatalog.ArgTokenScanTarget}, OutputParser: scanoutput.OutputParserSARIF,
	}
}

func TestCatalogStoreSerializesConcurrentDeviceCreates(t *testing.T) {
	root := configlayout.FindModuleRoot()
	home := t.TempDir()
	store := scancatalog.NewCatalogStore(root, home, nil)
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, entry := range []scancatalog.ScannerEntry{userScanner("custom-sca", "sca"), userScanner("custom-sast", "sast")} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- store.CreateUserExternalScanner(t.Context(), entry)
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		testutil.FailErr(t, "concurrent create", err)
	}
	overlay, err := scancatalog.LoadUserScannerOverlay(scancatalog.UserScannersPath(home))
	testutil.FailErr(t, "load overlay", err)
	if len(overlay.Scanners) != 2 {
		t.Fatalf("concurrent creates persisted %d rows, want 2", len(overlay.Scanners))
	}
}

func TestCatalogStoreRollsBackWhenRegistryPublicationFails(t *testing.T) {
	root := configlayout.FindModuleRoot()
	home := t.TempDir()
	wantErr := errors.New("registry reload failed")
	var calls atomic.Int64
	store := scancatalog.NewCatalogStore(root, home, func() error {
		if calls.Add(1) == 1 {
			return wantErr
		}
		return nil
	})
	err := store.CreateUserExternalScanner(t.Context(), userScanner("custom-sca", "sca"))
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want publication failure", err)
	}
	overlay, loadErr := scancatalog.LoadUserScannerOverlay(scancatalog.UserScannersPath(home))
	testutil.FailErr(t, "load rolled back overlay", loadErr)
	if len(overlay.Scanners) != 0 || calls.Load() != 2 {
		t.Fatalf("rollback rows=%d publication_calls=%d", len(overlay.Scanners), calls.Load())
	}
}

func TestMergeScannerCatalogProjectEnableOnly(t *testing.T) {
	enabled := true
	disabled := false
	host := &scancatalog.ScannerConfig{Scanners: []scancatalog.ScannerEntry{{
		ID: "lycaon-sca", Driver: scancatalog.DriverLibrary, Impl: "osv_scalibr", Engine: "osv-scalibr", ScopeKind: string(scancatalog.ScopeDependencies),
		Categories: []string{"sca"}, Enabled: &enabled,
	}}}
	project := &scancatalog.ScannerConfig{Scanners: []scancatalog.ScannerEntry{{
		ID: "lycaon-sca", Enabled: &disabled,
	}}}
	entries, rejected, err := scancatalog.MergeScannerCatalog(host, nil, project, scancatalog.MergeOptions{})
	testutil.FailErr(t, "MergeScannerCatalog", err)
	if len(rejected) != 0 {
		t.Fatalf("rejected = %+v", rejected)
	}
	if len(entries) != 1 || entries[0].EnabledOrDefault() {
		t.Fatalf("want disabled stock id, got %+v", entries)
	}
	if entries[0].CatalogSource != scancatalog.CatalogSourceProject {
		t.Fatalf("catalog_source = %q", entries[0].CatalogSource)
	}
}

func TestMergeScannerCatalogProjectCommandRejected(t *testing.T) {
	host := &scancatalog.ScannerConfig{Scanners: []scancatalog.ScannerEntry{{
		ID: "lycaon-sca", Driver: scancatalog.DriverLibrary, Impl: "osv_scalibr", Engine: "osv-scalibr", ScopeKind: string(scancatalog.ScopeDependencies), Categories: []string{"sca"},
	}}}
	project := &scancatalog.ScannerConfig{Scanners: []scancatalog.ScannerEntry{{
		ID: "lycaon-sca", Driver: scancatalog.DriverExternal, Categories: []string{"sca"},
		Command: []string{"echo", scancatalog.ArgTokenScanTarget}, OutputParser: scanoutput.OutputParserSARIF,
	}}}
	_, rejected, err := scancatalog.MergeScannerCatalog(host, nil, project, scancatalog.MergeOptions{})
	testutil.FailErr(t, "MergeScannerCatalog", err)
	if len(rejected) != 1 || rejected[0].Code != scancatalog.RejectProjectFieldsForbidden {
		t.Fatalf("want project_fields_forbidden, got %+v", rejected)
	}
}

func TestMergeScannerCatalogProjectUnknownID(t *testing.T) {
	host := &scancatalog.ScannerConfig{Scanners: []scancatalog.ScannerEntry{{
		ID: "lycaon-sca", Driver: scancatalog.DriverLibrary, Impl: "osv_scalibr", Engine: "osv-scalibr", ScopeKind: string(scancatalog.ScopeDependencies), Categories: []string{"sca"},
	}}}
	disabled := false
	project := &scancatalog.ScannerConfig{Scanners: []scancatalog.ScannerEntry{{
		ID: "nope", Enabled: &disabled,
	}}}
	_, rejected, err := scancatalog.MergeScannerCatalog(host, nil, project, scancatalog.MergeOptions{})
	testutil.FailErr(t, "MergeScannerCatalog", err)
	if len(rejected) != 1 || rejected[0].Code != scancatalog.RejectProjectUnknownID {
		t.Fatalf("want project_unknown_id, got %+v", rejected)
	}
}

func TestMergeScannerCatalogUserNewLibraryRejected(t *testing.T) {
	host := &scancatalog.ScannerConfig{Scanners: []scancatalog.ScannerEntry{{
		ID: "lycaon-sca", Driver: scancatalog.DriverLibrary, Impl: "osv_scalibr", Engine: "osv-scalibr", ScopeKind: string(scancatalog.ScopeDependencies), Categories: []string{"sca"},
	}}}
	user := &scancatalog.ScannerConfig{Scanners: []scancatalog.ScannerEntry{{
		ID: "evil-lib", Driver: scancatalog.DriverLibrary, Impl: "osv_scalibr", Categories: []string{"sca"},
	}}}
	_, rejected, err := scancatalog.MergeScannerCatalog(host, user, nil, scancatalog.MergeOptions{})
	testutil.FailErr(t, "MergeScannerCatalog", err)
	if len(rejected) != 1 || rejected[0].Code != scancatalog.RejectCommunityDriverForbidden {
		t.Fatalf("want community_driver_forbidden, got %+v", rejected)
	}
}

func TestMergeScannerCatalogUserExternalOK(t *testing.T) {
	host := &scancatalog.ScannerConfig{Scanners: []scancatalog.ScannerEntry{{
		ID: "lycaon-sca", Driver: scancatalog.DriverLibrary, Impl: "osv_scalibr", Engine: "osv-scalibr", ScopeKind: string(scancatalog.ScopeDependencies), Categories: []string{"sca"},
	}}}
	user := &scancatalog.ScannerConfig{Scanners: []scancatalog.ScannerEntry{{
		ID: "semgrep", Driver: scancatalog.DriverExternal, Engine: "semgrep", ScopeKind: string(scancatalog.ScopeSourceDriver), Categories: []string{"sast"},
		Command: []string{"semgrep", scancatalog.ArgTokenScanTarget}, OutputParser: scanoutput.OutputParserSARIF,
	}}}
	entries, rejected, err := scancatalog.MergeScannerCatalog(host, user, nil, scancatalog.MergeOptions{})
	testutil.FailErr(t, "MergeScannerCatalog", err)
	if len(rejected) != 0 {
		t.Fatalf("rejected = %+v", rejected)
	}
	found := false
	for _, e := range entries {
		if e.ID == "semgrep" && e.CatalogSource == scancatalog.CatalogSourceUser {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing user external: %+v", entries)
	}
}

func TestMergeScannerCatalogDisablesConflictingSlotOccupant(t *testing.T) {
	enabled := true
	host := &scancatalog.ScannerConfig{Scanners: []scancatalog.ScannerEntry{{
		ID: "stock", Driver: scancatalog.DriverLibrary, Impl: "stock", Engine: "stock", ScopeKind: string(scancatalog.ScopeSourceDriver), Categories: []string{"sast"}, Enabled: &enabled,
	}}}
	user := &scancatalog.ScannerConfig{Scanners: []scancatalog.ScannerEntry{{
		ID: "custom", Driver: scancatalog.DriverExternal, Engine: "custom", ScopeKind: string(scancatalog.ScopeSourceDriver), Categories: []string{"sast"},
		Command: []string{"custom", scancatalog.ArgTokenScanTarget}, OutputParser: scanoutput.OutputParserSARIF, Enabled: &enabled,
	}}}
	entries, rejected, err := scancatalog.MergeScannerCatalog(host, user, nil, scancatalog.MergeOptions{})
	testutil.FailErr(t, "MergeScannerCatalog", err)
	if len(rejected) != 1 || rejected[0].ID != "custom" || rejected[0].Code != scancatalog.RejectSlotConflict {
		t.Fatalf("rejected = %+v, want custom slot conflict", rejected)
	}
	selected := ""
	for _, entry := range entries {
		if entry.EnabledOrDefault() {
			if selected != "" {
				t.Fatalf("multiple enabled SAST scanners: %q and %q", selected, entry.ID)
			}
			selected = entry.ID
		}
	}
	if selected != "stock" {
		t.Fatalf("selected = %q, want deterministic first occupant", selected)
	}
}

func TestSetProjectScannerEnabledReplacesWholeSlotAtomically(t *testing.T) {
	root := configlayout.FindModuleRoot()
	home := t.TempDir()
	projectDir := t.TempDir()
	store := scancatalog.NewCatalogStore(root, home, nil)
	disabled := false
	err := store.CreateUserExternalScanner(t.Context(), scancatalog.ScannerEntry{
		ID: "custom-sca", Engine: "custom", ScopeKind: string(scancatalog.ScopeDependencies), Categories: []string{"sca"}, Command: []string{"true", scancatalog.ArgTokenScanTarget},
		OutputParser: scanoutput.OutputParserSARIF, Enabled: &disabled,
	})
	testutil.FailErr(t, "CreateUserExternalScanner", err)
	testutil.FailErr(t, "SetProjectScannerEnabled",
		store.SetProjectScannerEnabled(t.Context(), projectDir, "custom-sca", true))

	cfg, err := scancatalog.LoadMergedScannerConfig(root, projectDir, home)
	testutil.FailErr(t, "LoadMergedScannerConfig", err)
	selected := ""
	for _, entry := range cfg.Scanners {
		if scancatalog.PrimaryCategory(entry.Categories) != "sca" || !entry.EnabledOrDefault() {
			continue
		}
		if selected != "" {
			t.Fatalf("SCA slot has both %q and %q enabled", selected, entry.ID)
		}
		selected = entry.ID
	}
	if selected != "custom-sca" {
		t.Fatalf("selected SCA scanner = %q, want custom-sca", selected)
	}
	overlay, err := scancatalog.LoadUserScannerOverlay(scancatalog.ProjectScannersPath(projectDir))
	testutil.FailErr(t, "LoadUserScannerOverlay", err)
	states := make(map[string]bool, len(overlay.Scanners))
	for _, row := range overlay.Scanners {
		states[row.ID] = row.EnabledOrDefault()
	}
	if !states["custom-sca"] || states["lycaon-sca"] {
		t.Fatalf("project slot rows = %+v, want custom true and stock false", states)
	}
}

func TestSaveDeviceScannerEntryReplacesWholeSlotAtomically(t *testing.T) {
	root := configlayout.FindModuleRoot()
	home := t.TempDir()
	store := scancatalog.NewCatalogStore(root, home, nil)
	enabled := true
	entry := scancatalog.ScannerEntry{
		ID: "custom-sca", Driver: scancatalog.DriverExternal, Engine: "custom", ScopeKind: string(scancatalog.ScopeDependencies), Categories: []string{"sca"},
		Command: []string{"true", scancatalog.ArgTokenScanTarget}, OutputParser: scanoutput.OutputParserSARIF, Enabled: &enabled,
	}
	testutil.FailErr(t, "SaveDeviceScannerEntry", store.SaveDeviceScannerEntry(t.Context(), entry))

	cfg, err := scancatalog.LoadMergedScannerConfig(root, "", home)
	testutil.FailErr(t, "LoadMergedScannerConfig", err)
	selected := ""
	for _, candidate := range cfg.Scanners {
		if scancatalog.PrimaryCategory(candidate.Categories) != "sca" || !candidate.EnabledOrDefault() {
			continue
		}
		if selected != "" {
			t.Fatalf("SCA slot has both %q and %q enabled", selected, candidate.ID)
		}
		selected = candidate.ID
	}
	if selected != "custom-sca" {
		t.Fatalf("selected SCA scanner = %q, want custom-sca", selected)
	}
}

func TestCreateUserExternalScannerAlwaysStartsDisabled(t *testing.T) {
	root := configlayout.FindModuleRoot()
	home := t.TempDir()
	store := scancatalog.NewCatalogStore(root, home, nil)
	enabled := true
	err := store.CreateUserExternalScanner(t.Context(), scancatalog.ScannerEntry{
		ID: "custom-sca", Engine: "custom", ScopeKind: string(scancatalog.ScopeDependencies), Categories: []string{"sca"}, Command: []string{"true", scancatalog.ArgTokenScanTarget},
		OutputParser: scanoutput.OutputParserSARIF, Enabled: &enabled,
	})
	testutil.FailErr(t, "CreateUserExternalScanner", err)

	cfg, err := scancatalog.LoadMergedScannerConfig(root, "", home)
	testutil.FailErr(t, "LoadMergedScannerConfig", err)
	for _, candidate := range cfg.Scanners {
		if candidate.ID == "custom-sca" && candidate.EnabledOrDefault() {
			t.Fatal("new user scanner bypassed slot selection")
		}
	}
}

func TestMergeScannerCatalogUserStockParserForbidden(t *testing.T) {
	host := &scancatalog.ScannerConfig{Scanners: []scancatalog.ScannerEntry{{
		ID: "lycaon-sca", Driver: scancatalog.DriverLibrary, Impl: "osv_scalibr", Engine: "osv-scalibr", ScopeKind: string(scancatalog.ScopeDependencies), Categories: []string{"sca"},
	}}}
	// User rows reject the bundled parser.
	deny := []string{
		scanoutput.OutputParserOpengrepJSON,
	}
	for _, parser := range deny {
		user := &scancatalog.ScannerConfig{Scanners: []scancatalog.ScannerEntry{{
			ID: "mytool", Driver: scancatalog.DriverExternal, Engine: "mytool", ScopeKind: string(scancatalog.ScopeSourceDriver), Categories: []string{"sast"},
			Command: []string{"mytool", scancatalog.ArgTokenScanTarget}, OutputParser: parser,
		}}}
		_, rejected, err := scancatalog.MergeScannerCatalog(host, user, nil, scancatalog.MergeOptions{})
		testutil.FailErr(t, "MergeScannerCatalog", err)
		if len(rejected) != 1 || rejected[0].Code != scancatalog.RejectCommunityParserForbidden {
			t.Fatalf("parser %q: want community_parser_forbidden, got %+v", parser, rejected)
		}
	}
}

func TestMergeScannerCatalogUserFindingsJSONAllowed(t *testing.T) {
	host := &scancatalog.ScannerConfig{Scanners: []scancatalog.ScannerEntry{{
		ID: "lycaon-sca", Driver: scancatalog.DriverLibrary, Impl: "osv_scalibr", Engine: "osv-scalibr", ScopeKind: string(scancatalog.ScopeDependencies), Categories: []string{"sca"},
	}}}
	user := &scancatalog.ScannerConfig{Scanners: []scancatalog.ScannerEntry{{
		ID: "mytool", Driver: scancatalog.DriverExternal, Engine: "mytool", ScopeKind: string(scancatalog.ScopeSourceDriver), Categories: []string{"sast"},
		Command: []string{"mytool", scancatalog.ArgTokenScanTarget}, OutputParser: scanoutput.OutputParserFindingsJSON,
	}}}
	entries, rejected, err := scancatalog.MergeScannerCatalog(host, user, nil, scancatalog.MergeOptions{})
	testutil.FailErr(t, "MergeScannerCatalog", err)
	if len(rejected) != 0 {
		t.Fatalf("rejected = %+v", rejected)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %+v", entries)
	}
}

func TestMergeScannerCatalogMapJSONMapperMissing(t *testing.T) {
	host := &scancatalog.ScannerConfig{Scanners: []scancatalog.ScannerEntry{{
		ID: "lycaon-sca", Driver: scancatalog.DriverLibrary, Impl: "osv_scalibr", Engine: "osv-scalibr", ScopeKind: string(scancatalog.ScopeDependencies), Categories: []string{"sca"},
	}}}
	user := &scancatalog.ScannerConfig{Scanners: []scancatalog.ScannerEntry{
		{
			ID: "no-mapper-id", Driver: scancatalog.DriverExternal, Engine: "mytool", ScopeKind: string(scancatalog.ScopeSourceDriver), Categories: []string{"sast"},
			Command: []string{"mytool", scancatalog.ArgTokenScanTarget}, OutputParser: scanoutput.OutputParserMapJSON,
		},
		{
			ID: "mapper-file-absent", Driver: scancatalog.DriverExternal, Engine: "mytool", ScopeKind: string(scancatalog.ScopeSourceDriver), Categories: []string{"sast"},
			Command: []string{"mytool", scancatalog.ArgTokenScanTarget}, OutputParser: scanoutput.OutputParserMapJSON, MapperID: "ghost",
		},
	}}
	_, rejected, err := scancatalog.MergeScannerCatalog(host, user, nil, scancatalog.MergeOptions{UserConfigDir: t.TempDir()})
	testutil.FailErr(t, "MergeScannerCatalog", err)
	if len(rejected) != 2 {
		t.Fatalf("rejected = %+v", rejected)
	}
	for _, r := range rejected {
		if r.Code != scancatalog.RejectMapperMissing {
			t.Fatalf("want mapper_missing, got %+v", r)
		}
	}
}

func TestMergeScannerCatalogMapJSONMapperPresent(t *testing.T) {
	configDir := t.TempDir()
	mapperDir := filepath.Join(configDir, "scanners", "mappers")
	if err := os.MkdirAll(mapperDir, 0o755); err != nil {
		testutil.FailErr(t, "mkdir mappers", err)
	}
	body := "id: mytool_lite\nstdout_format: json\nitems_path: /results\nfields:\n  rule_id: /check_id\n  level: /severity\n"
	if err := os.WriteFile(filepath.Join(mapperDir, "mytool_lite.yaml"), []byte(body), 0o644); err != nil {
		testutil.FailErr(t, "write mapper", err)
	}
	host := &scancatalog.ScannerConfig{Scanners: []scancatalog.ScannerEntry{{
		ID: "lycaon-sca", Driver: scancatalog.DriverLibrary, Impl: "osv_scalibr", Engine: "osv-scalibr", ScopeKind: string(scancatalog.ScopeDependencies), Categories: []string{"sca"},
	}}}
	user := &scancatalog.ScannerConfig{Scanners: []scancatalog.ScannerEntry{{
		ID: "mytool", Driver: scancatalog.DriverExternal, Engine: "mytool", ScopeKind: string(scancatalog.ScopeSourceDriver), Categories: []string{"sast"},
		Command: []string{"mytool", scancatalog.ArgTokenScanTarget}, OutputParser: scanoutput.OutputParserMapJSON, MapperID: "mytool_lite",
	}}}
	entries, rejected, err := scancatalog.MergeScannerCatalog(host, user, nil, scancatalog.MergeOptions{UserConfigDir: configDir})
	testutil.FailErr(t, "MergeScannerCatalog", err)
	if len(rejected) != 0 {
		t.Fatalf("rejected = %+v", rejected)
	}
	found := false
	for _, e := range entries {
		if e.ID == "mytool" && e.MapperID == "mytool_lite" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing map/json user row: %+v", entries)
	}
}

func TestLoadMergedScannerConfigRejectsProjectCommand(t *testing.T) {
	root := configlayout.FindModuleRoot()
	projectDir := t.TempDir()
	lycaonDir := filepath.Join(projectDir, settingsoverlay.DirName())
	if err := os.MkdirAll(lycaonDir, 0o755); err != nil {
		testutil.FailErr(t, "mkdir", err)
	}
	// Project overlays are enable-only.
	body := "scanners:\n  - id: lycaon-sca\n    enabled: false\n"
	if err := os.WriteFile(filepath.Join(lycaonDir, "scanners.yaml"), []byte(body), 0o644); err != nil {
		testutil.FailErr(t, "write", err)
	}
	cfg, err := scancatalog.LoadMergedScannerConfig(root, projectDir, t.TempDir())
	testutil.FailErr(t, "LoadMergedScannerConfig", err)
	for _, s := range cfg.Scanners {
		if s.ID == "lycaon-sca" && s.EnabledOrDefault() {
			t.Fatal("expected lycaon-sca disabled via project enable-only")
		}
	}
}
