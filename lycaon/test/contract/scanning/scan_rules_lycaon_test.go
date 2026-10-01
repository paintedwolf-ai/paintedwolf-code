package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	scancatalog "github.com/lycaon/lycaon/internal/scan/catalog"
	"github.com/lycaon/lycaon/internal/scan/rules"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestLycaonGateRulesContract(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	lycaonRoot := filepath.Join(root, "lycaon")

	gates, err := rules.LoadOpengrepGates()
	contractcheck.FailErr(t, "rules.LoadOpengrepGates failed", err)

	prov, err := rules.LoadRulesProvenance()
	contractcheck.FailErr(t, "rules.LoadRulesProvenance failed", err)

	if prov.Lycaon.License != "CC-BY-4.0" {
		t.Fatalf("lycaon provenance license = %q, want CC-BY-4.0", prov.Lycaon.License)
	}
	if prov.Lycaon.Origin != rules.LycaonProvenanceOriginBundledInRepo {
		t.Fatalf("lycaon provenance origin = %q, want %q", prov.Lycaon.Origin, rules.LycaonProvenanceOriginBundledInRepo)
	}
	if prov.Lycaon.Authorship != "in-repo" {
		t.Fatalf("lycaon provenance authorship = %q, want in-repo", prov.Lycaon.Authorship)
	}

	licensePath := filepath.Join(lycaonRoot, "config", "runtime", "scanners", "rules", "lycaon", "LICENSE")
	if _, err := os.Stat(licensePath); err != nil {
		t.Fatalf("missing lycaon LICENSE: %v", err)
	}

	provPaths := make(map[string]struct{}, len(prov.Lycaon.Files))
	for _, f := range prov.Lycaon.Files {
		if f.Path == "" {
			t.Fatal("provenance file entry missing path")
		}
		provPaths[f.Path] = struct{}{}
		abs := rules.ResolveModulePath(lycaonRoot, f.Path)
		if _, err := os.Stat(abs); err != nil {
			t.Fatalf("provenance path missing on disk: %s: %v", f.Path, err)
		}
		if f.Test != "" {
			testAbs := rules.ResolveModulePath(lycaonRoot, f.Test)
			if _, err := os.Stat(testAbs); err != nil {
				t.Fatalf("provenance test path missing: %s: %v", f.Test, err)
			}
		}
	}

	active := rules.ActiveLycaonGatePaths(gates)

	for _, rel := range active {
		if !strings.Contains(rel, "config/runtime/scanners/rules/lycaon/") {
			t.Fatalf("lycaon gate path %q must live under config/runtime/scanners/rules/lycaon/", rel)
		}
		abs := rules.ResolveModulePath(lycaonRoot, rel)
		if _, err := os.Stat(abs); err != nil {
			t.Fatalf("opengrep-gates lycaon path missing: %s: %v", rel, err)
		}
		if _, ok := provPaths[rel]; !ok {
			t.Fatalf("active gate path %q not listed in rules-provenance.yaml lycaon.files", rel)
		}
		if err := rules.ValidateRuleFile(abs); err != nil {
			t.Fatalf("validate %s: %v", rel, err)
		}
	}

	lycaonRulesRoot := filepath.Join(lycaonRoot, "config", "runtime", "scanners", "rules", "lycaon")
	if _, err := rules.CollectRuleIDsFromDir(lycaonRulesRoot); err != nil {
		t.Fatalf("duplicate rule ids in lycaon tree: %v", err)
	}
	_ = filepath.WalkDir(lycaonRulesRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".yaml") || strings.HasSuffix(path, ".test.yaml") {
			return nil
		}
		rf, err := rules.LoadRuleFile(path)
		if err != nil {
			return err
		}
		for _, r := range rf.Rules {
			if strings.EqualFold(r.Severity, "INFO") && (r.Metadata == nil || r.Metadata.Purpose != "coverage") {
				t.Errorf("%s: rule %q uses forbidden INFO severity", path, r.ID)
			}
		}
		return nil
	})
}

func TestScannersYAMLReferencesOpengrepGates(t *testing.T) {
	t.Parallel()
	cfg, err := scancatalog.LoadScannerConfig()
	contractcheck.FailErr(t, "scan.LoadScannerConfig failed", err)
	var sast *scancatalog.ScannerEntry
	for i := range cfg.Scanners {
		if cfg.Scanners[i].ID == "lycaon-sast" {
			sast = &cfg.Scanners[i]
			break
		}
	}
	if sast == nil {
		t.Fatal("missing lycaon-sast scanner")
	}
	want := "config/runtime/scanners/opengrep-gates.yaml"
	if sast.Config != want {
		t.Fatalf("lycaon-sast config = %q, want %q", sast.Config, want)
	}
}
