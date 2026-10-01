package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	scancatalog "github.com/lycaon/lycaon/internal/scan/catalog"
	"github.com/lycaon/lycaon/internal/scan/registry"
	"github.com/lycaon/lycaon/internal/scan/rules"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestScannerConfigContract(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	lycaonRoot := filepath.Join(root, "lycaon")
	cfg, err := scancatalog.LoadScannerConfig()
	contractcheck.FailErr(t, "scancatalog.LoadScannerConfig failed", err)
	if err := scancatalog.ValidateScannerConfig(cfg); err != nil {
		contractcheck.FailErr(t, "scancatalog.ValidateScannerConfig failed", err)
	}
	reg, err := registry.New(registry.Options{ModuleRoot: lycaonRoot})
	contractcheck.FailErr(t, "registry.New failed", err)
	if len(reg.List()) < 3 {
		t.Fatalf("expected >=3 enabled scanners, got %d", len(reg.List()))
	}
}

func TestGateRulePathsExist(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	lycaonRoot := filepath.Join(root, "lycaon")
	gates, err := rules.LoadOpengrepGates()
	contractcheck.FailErr(t, "rules.LoadOpengrepGates failed", err)
	paths, err := rules.MaterializeGateRules(gates, lycaonRoot, t.TempDir())
	contractcheck.FailErr(t, "rules.MaterializeGateRules failed", err)
	if len(paths) == 0 {
		t.Fatal("expected gate rule paths")
	}
}

var wantRulesProvenanceVendorIDs = []string{
	"patched-codes",
	"dgryski-go",
	"elttam",
	"0xdea",
	"dotta",
	"apiiro-malicious",
}

func TestRulesProvenanceVendorsSchema(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	lycaonRoot := filepath.Join(root, "lycaon")
	provPath := filepath.Join(lycaonRoot, "config", "runtime", "scanners", "rules-provenance.yaml")
	raw, err := os.ReadFile(provPath)
	contractcheck.FailErr(t, "read file", err)
	if !strings.Contains(string(raw), "\nvendors:") && !strings.HasPrefix(string(raw), "vendors:") {
		t.Fatal("rules-provenance.yaml missing vendors: block")
	}

	prov, err := rules.LoadRulesProvenance()
	contractcheck.FailErr(t, "rules.LoadRulesProvenance failed", err)
	if len(prov.Vendors) != len(wantRulesProvenanceVendorIDs) {
		t.Fatalf("vendor catalog count = %d, want %d", len(prov.Vendors), len(wantRulesProvenanceVendorIDs))
	}
	got := make(map[string]struct{}, len(prov.Vendors))
	for _, vendor := range prov.Vendors {
		got[vendor.ID] = struct{}{}
	}
	for _, want := range wantRulesProvenanceVendorIDs {
		if _, ok := got[want]; !ok {
			t.Fatalf("rules-provenance vendors missing id %q", want)
		}
	}
}

func TestRulesProvenanceVendorMIT(t *testing.T) {
	t.Parallel()
	prov, err := rules.LoadRulesProvenance()
	contractcheck.FailErr(t, "rules.LoadRulesProvenance failed", err)
	if prov.Lycaon.License != "CC-BY-4.0" {
		t.Fatalf("lycaon license = %q", prov.Lycaon.License)
	}
	if prov.Lycaon.Origin != rules.LycaonProvenanceOriginBundledInRepo {
		t.Fatalf("lycaon origin = %q, want %q", prov.Lycaon.Origin, rules.LycaonProvenanceOriginBundledInRepo)
	}
	if len(prov.Vendors) != len(wantRulesProvenanceVendorIDs) {
		t.Fatalf("vendor catalog count = %d, want %d", len(prov.Vendors), len(wantRulesProvenanceVendorIDs))
	}
	for _, vendor := range prov.Vendors {
		if strings.ToUpper(vendor.License) != "MIT" {
			t.Fatalf("vendor catalog %q license = %q, want MIT", vendor.ID, vendor.License)
		}
		if vendor.Commit == "" {
			t.Fatalf("vendor catalog %q missing commit pin", vendor.ID)
		}
	}
}
