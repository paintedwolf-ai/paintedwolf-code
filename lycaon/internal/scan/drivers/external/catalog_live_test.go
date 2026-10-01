//go:build live_scanners

package external

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/scan"
	scancatalog "github.com/lycaon/lycaon/internal/scan/catalog"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCatalogScannerLive(t *testing.T) {
	if testing.Short() {
		t.Skip("live scanner test skipped under -short")
	}
	moduleRoot := configlayout.FindModuleRoot()
	cat, err := scancatalog.LoadScannerCatalog()
	if err != nil {
		testutil.FailErr(t, "load scanner catalog", err)
	}

	projectDir := seedLiveScanProject(t)

	var ran int
	for _, catalogEntry := range cat.Entries() {
		if _, lookErr := exec.LookPath(catalogEntry.Binary); lookErr != nil {
			continue
		}
		entry, ok := cat.ScannerEntryFor(catalogEntry.ID)
		if !ok {
			t.Fatalf("ScannerEntryFor(%q) not found", catalogEntry.ID)
		}
		t.Run(catalogEntry.ID, func(t *testing.T) {
			scanner, err := NewScanner(entry, moduleRoot, "", "")
			if err != nil {
				testutil.FailErr(t, "new scanner "+catalogEntry.ID, err)
			}
			result, err := scanner.Run(context.Background(), scan.ScanRequest{ProjectDir: projectDir})
			if err != nil {
				testutil.FailErr(t, "run installed scanner "+catalogEntry.ID, err)
			}
			if result == nil {
				t.Fatalf("%s returned no result", catalogEntry.ID)
			}
			if len(result.Categories) == 0 {
				t.Fatalf("%s returned no categories", catalogEntry.ID)
			}
			t.Logf("%s parsed cleanly: %d findings", catalogEntry.ID, result.FindingsCount)
		})
		ran++
	}
	if ran == 0 {
		t.Skip("no catalog scanner binaries installed on this device")
	}
}

// seedLiveScanProject satisfies every catalog input shape.
func seedLiveScanProject(t *testing.T) string {
	t.Helper()
	projectDir := t.TempDir()
	testutil.FailErr(t, "seed Go module", os.WriteFile(filepath.Join(projectDir, "go.mod"),
		[]byte("module scanner-fixture\n\ngo 1.26.0\n"), 0o600))
	testutil.FailErr(t, "seed Go source", os.WriteFile(filepath.Join(projectDir, "main.go"),
		[]byte("package main\n\nfunc main() {}\n"), 0o600))
	if err := os.WriteFile(filepath.Join(projectDir, "requirements.txt"),
		[]byte("requests==2.19.1\n"), 0o600); err != nil {
		testutil.FailErr(t, "seed lockfile", err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "app.js"),
		[]byte("const x = eval(userInput);\n"), 0o600); err != nil {
		testutil.FailErr(t, "seed source file", err)
	}
	rule := "rules:\n" +
		"  - id: live-eval\n" +
		"    patterns:\n" +
		"      - pattern: eval(...)\n" +
		"    message: eval detected\n" +
		"    languages: [javascript]\n" +
		"    severity: WARNING\n"
	for _, dir := range []string{".opengrep", ".semgrep"} {
		ruleDir := filepath.Join(projectDir, dir)
		if err := os.MkdirAll(ruleDir, 0o750); err != nil {
			testutil.FailErr(t, "seed rule dir "+dir, err)
		}
		if err := os.WriteFile(filepath.Join(ruleDir, "rules.yml"), []byte(rule), 0o600); err != nil {
			testutil.FailErr(t, "seed rule file "+dir, err)
		}
	}
	return projectDir
}
