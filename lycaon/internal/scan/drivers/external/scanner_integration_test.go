//go:build integration

package external_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/scan"
	scancatalog "github.com/lycaon/lycaon/internal/scan/catalog"
	"github.com/lycaon/lycaon/internal/scan/drivers/external"
	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	"github.com/lycaon/lycaon/internal/scan/registry"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// Fixture binaries live outside the scanned project.
func TestExternalScannerRunsFindingsJSONFakeCLI(t *testing.T) {
	root := configlayout.FindModuleRoot()
	projectDir := t.TempDir()
	script := filepath.Join(root, "internal", "scan", "testdata", "fake-findings-scanner.sh")
	entry := scancatalog.ScannerEntry{
		ID:           "byok-findings",
		Driver:       scancatalog.DriverExternal,
		Categories:   []string{"security"},
		Command:      []string{script},
		OutputParser: scanoutput.OutputParserFindingsJSON,
	}
	sc, err := external.NewScanner(entry, root, "", "")
	testutil.FailErr(t, "external.NewScanner failed", err)
	res, err := sc.Run(context.Background(), scan.ScanRequest{ProjectDir: projectDir})
	testutil.FailErr(t, "sc.Run failed", err)
	if res.FindingsCount != 1 {
		t.Fatalf("findings = %d", res.FindingsCount)
	}
	got := res.Findings[0]
	if got.RuleID != "byok-findings:fixture-rule" {
		t.Fatalf("rule id = %q", got.RuleID)
	}
	if got.Locations[0].URI != "src/app.py" || got.Locations[0].StartLine != 7 {
		t.Fatalf("location = %+v", got.Locations[0])
	}
}

func TestExternalScannerRunsSARIFFakeCLI(t *testing.T) {
	root := configlayout.FindModuleRoot()
	projectDir := t.TempDir()
	script := filepath.Join(root, "internal", "scan", "testdata", "fake-sarif-scanner.sh")
	entry := scancatalog.ScannerEntry{
		ID:           "byok-sarif",
		Driver:       scancatalog.DriverExternal,
		Categories:   []string{"security"},
		Command:      []string{script},
		OutputParser: scanoutput.OutputParserSARIF,
	}
	sc, err := external.NewScanner(entry, root, "", "")
	testutil.FailErr(t, "external.NewScanner failed", err)
	res, err := sc.Run(context.Background(), scan.ScanRequest{ProjectDir: projectDir})
	testutil.FailErr(t, "sc.Run failed", err)
	if res.FindingsCount != 1 {
		t.Fatalf("findings = %d", res.FindingsCount)
	}
	got := res.Findings[0]
	if got.RuleID != "byok-sarif:hardcoded-password" {
		t.Fatalf("rule id = %q", got.RuleID)
	}
	if got.Locations[0].URI != "src/auth.go" || got.Locations[0].StartLine != 21 {
		t.Fatalf("location = %+v", got.Locations[0])
	}
}

func TestExternalScannerRunsEverySelectedPath(t *testing.T) {
	root := configlayout.FindModuleRoot()
	projectDir := t.TempDir()
	for _, path := range []string{"src/a", "src/b"} {
		if err := os.MkdirAll(filepath.Join(projectDir, path), 0o755); err != nil {
			testutil.FailErr(t, "create scan target", err)
		}
	}
	script := filepath.Join(root, "internal", "scan", "testdata", "fake-sarif-scanner.sh")
	entry := scancatalog.ScannerEntry{
		ID: "path-aware", Driver: scancatalog.DriverExternal, Engine: "fake",
		ScopeKind: string(scancatalog.ScopeSourceDriver), Categories: []string{"sast", "security"},
		Command: []string{script, scancatalog.ArgTokenScanTarget}, OutputParser: scanoutput.OutputParserSARIF,
	}
	scanner, err := external.NewScanner(entry, root, "", "")
	testutil.FailErr(t, "external.NewScanner", err)
	result, err := scanner.Run(t.Context(), scan.ScanRequest{
		ProjectDir: projectDir, Paths: []string{"src/a", "src/b"},
	})
	testutil.FailErr(t, "scan selected paths", err)
	if result.FindingsCount != 2 || len(result.Findings) != 2 {
		t.Fatalf("findings = %d/%d want one result per target", result.FindingsCount, len(result.Findings))
	}
	for _, finding := range result.Findings {
		if finding.Properties == nil || finding.Properties.Lycaon == nil ||
			finding.Properties.Lycaon.Kind != api.FindingKindSAST {
			t.Fatalf("finding classification = %#v", finding.Properties)
		}
	}
}

// A nonzero exit invalidates an otherwise well-formed report.
func TestExternalScannerRejectsExitOutsideDeclaredCodes(t *testing.T) {
	root := configlayout.FindModuleRoot()
	projectDir := t.TempDir()
	script := filepath.Join(root, "internal", "scan", "testdata", "fake-badconfig-scanner.sh")
	entry := scancatalog.ScannerEntry{
		ID:           "byok-badconfig",
		Driver:       scancatalog.DriverExternal,
		Categories:   []string{"security"},
		Command:      []string{script},
		OutputParser: scanoutput.OutputParserSARIF,
		OkExitCodes:  []int{0},
	}
	sc, err := external.NewScanner(entry, root, "", "")
	testutil.FailErr(t, "external.NewScanner failed", err)
	res, err := sc.Run(context.Background(), scan.ScanRequest{ProjectDir: projectDir})
	if err == nil {
		t.Fatalf("exit 7 accepted as a clean scan: %d findings", res.FindingsCount)
	}
	if !strings.Contains(err.Error(), "exit 7") {
		t.Fatalf("error does not name the exit code: %v", err)
	}
}

// Empty ok_exit_codes accepts every exit code.
func TestExternalScannerWithoutExitContractToleratesNonZero(t *testing.T) {
	root := configlayout.FindModuleRoot()
	projectDir := t.TempDir()
	script := filepath.Join(root, "internal", "scan", "testdata", "fake-badconfig-scanner.sh")
	entry := scancatalog.ScannerEntry{
		ID:           "byok-unverified",
		Driver:       scancatalog.DriverExternal,
		Categories:   []string{"security"},
		Command:      []string{script},
		OutputParser: scanoutput.OutputParserSARIF,
	}
	sc, err := external.NewScanner(entry, root, "", "")
	testutil.FailErr(t, "external.NewScanner failed", err)
	res, err := sc.Run(context.Background(), scan.ScanRequest{ProjectDir: projectDir})
	testutil.FailErr(t, "sc.Run failed", err)
	if res.FindingsCount != 0 {
		t.Fatalf("findings = %d, want 0", res.FindingsCount)
	}
}

func TestExternalScannerRunsMapJSONFakeCLI(t *testing.T) {
	root := configlayout.FindModuleRoot()
	projectDir := t.TempDir()
	configDir := t.TempDir()
	mapperDir := filepath.Join(configDir, "scanners", "mappers")
	if err := os.MkdirAll(mapperDir, 0o755); err != nil {
		testutil.FailErr(t, "mkdir mappers", err)
	}
	mapper := `id: semgrep_sarif_lite
stdout_format: json
items_path: /results
fields:
  rule_id: /check_id
  level: /extra/severity
  message: /extra/message
  uri: /path
  start_line: /start/line
level_map:
  ERROR: high
`
	if err := os.WriteFile(filepath.Join(mapperDir, "semgrep_sarif_lite.yaml"), []byte(mapper), 0o644); err != nil {
		testutil.FailErr(t, "write mapper", err)
	}
	script := filepath.Join(root, "internal", "scan", "testdata", "fake-mapped-scanner.sh")
	entry := scancatalog.ScannerEntry{
		ID:           "byok-mapped",
		Driver:       scancatalog.DriverExternal,
		Categories:   []string{"security"},
		Command:      []string{script},
		OutputParser: scanoutput.OutputParserMapJSON,
		MapperID:     "semgrep_sarif_lite",
	}
	sc, err := external.NewScanner(entry, root, configDir, "")
	testutil.FailErr(t, "external.NewScanner failed", err)
	res, err := sc.Run(context.Background(), scan.ScanRequest{ProjectDir: projectDir})
	testutil.FailErr(t, "sc.Run failed", err)
	if res.FindingsCount != 1 {
		t.Fatalf("findings = %d", res.FindingsCount)
	}
	got := res.Findings[0]
	if got.RuleID != "byok-mapped:py.sql-injection" {
		t.Fatalf("rule id = %q", got.RuleID)
	}
	if got.Level != "high" {
		t.Fatalf("level = %q", got.Level)
	}
	if got.Locations[0].URI != "app/db.py" || got.Locations[0].StartLine != 12 {
		t.Fatalf("location = %+v", got.Locations[0])
	}
}

func TestExternalScannerWritesProjectTree(t *testing.T) {
	if !confine.Available() {
		t.Skip("OS confinement unavailable")
	}
	confine.TestingSetAutoConfine(t)
	t.Setenv("LYCAON_BYPASS_APPROVALS", "")
	t.Setenv("LYCAON_SANDBOX", "")

	root := configlayout.FindModuleRoot()
	projectDir := t.TempDir()
	script := filepath.Join(root, "internal", "scan", "testdata", "fake-project-writer.sh")
	entry := scancatalog.ScannerEntry{
		ID:           "writer",
		Driver:       scancatalog.DriverExternal,
		Engine:       "writer",
		ScopeKind:    string(scancatalog.ScopeSourceDriver),
		Categories:   []string{"sast", "security"},
		Command:      []string{script, scancatalog.ArgTokenScanTarget},
		OutputParser: scanoutput.OutputParserSARIF,
	}
	sc, err := external.NewScanner(entry, root, "", "")
	testutil.FailErr(t, "external.NewScanner", err)
	if _, err := sc.Run(context.Background(), scan.ScanRequest{ProjectDir: projectDir}); err != nil {
		testutil.FailErr(t, "run scanner", err)
	}
	wrote := filepath.Join(projectDir, "from-scanner")
	if _, err := os.Stat(wrote); err != nil {
		t.Fatalf("external scanner did not write %s: %v", wrote, err)
	}
}

func TestExternalScannerMapJSONMissingMapperFailsConstruction(t *testing.T) {
	root := configlayout.FindModuleRoot()
	entry := scancatalog.ScannerEntry{
		ID:           "byok-mapped",
		Driver:       scancatalog.DriverExternal,
		Categories:   []string{"security"},
		Command:      []string{"mytool"},
		OutputParser: scanoutput.OutputParserMapJSON,
		MapperID:     "ghost",
	}
	if _, err := external.NewScanner(entry, root, t.TempDir(), ""); err == nil {
		t.Fatal("expected mapper load failure at construction")
	}
}

func TestRegistrySkipsMissingBinaryWhenConfigured(t *testing.T) {
	cfg := &scancatalog.ScannerConfig{Scanners: []scancatalog.ScannerEntry{{
		ID: "missing", Driver: scancatalog.DriverExternal, Engine: "missing", ScopeKind: string(scancatalog.ScopeSourceDriver), Categories: []string{"sast", "security"},
		Command: []string{"definitely-not-a-scanner-binary-xyz", scancatalog.ArgTokenScanTarget}, OutputParser: scanoutput.OutputParserSARIF,
	}}}
	reg, err := registry.NewFromScannerConfig(t.Context(), cfg, registry.Options{ModuleRoot: configlayout.FindModuleRoot()})
	testutil.FailErr(t, "registry.NewFromScannerConfig failed", err)
	if len(reg.List()) != 0 {
		t.Fatalf("expected skip, got %v", reg.List())
	}
}
