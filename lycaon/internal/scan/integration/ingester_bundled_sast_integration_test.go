//go:build integration

package integration

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/internal/scan/bundled"
	scancfg "github.com/lycaon/lycaon/internal/scan/configuration"
	bundleddriver "github.com/lycaon/lycaon/internal/scan/drivers/bundled"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// Guidance persistence and lookup over a real TLS verification finding.
func TestIngesterBundledSASTProducesGuidance(t *testing.T) {
	testutil.SkipIfShort(t, "runs real opengrep over the scan fixture")
	confine.TestingSetAutoConfine(t)
	corpus := scanFixtureCorpus(t)
	home, err := configdir.UserConfigDir()
	testutil.FailErr(t, "UserConfigDir", err)
	manifest, err := bundled.LoadManifest()
	testutil.FailErr(t, "LoadManifest", err)
	if _, err := bundled.ResolveOpenGrepBinary(manifest, home, configlayout.EngineRoot()); err != nil {
		testutil.MissingScannerResource(t, "opengrep", err)
	}

	sc := bundleddriver.NewOpenGrepScanner(bundleddriver.OpenGrepOptions{
		HomeDir:  home,
		Manifest: manifest,
	})
	res, err := sc.Run(context.Background(), scan.ScanRequest{
		ProjectDir: corpus,
		Categories: []api.ScanCategory{api.ScanCategorySAST},
	})
	testutil.FailErr(t, "opengrep Run", err)
	if len(res.Findings) == 0 {
		t.Fatalf("expected findings slice, count=%d, scanned=%v, warnings=%+v", res.FindingsCount, res.ScannedPaths, res.Warnings)
	}

	ing := &scan.IngesterImpl{
		Module:                  scancfg.DefaultModuleConfig(),
		Budget:                  scancfg.NewFindingBudget(scancfg.DefaultAgentBudget()),
		RecordWithoutDelegation: true,
	}
	rec, err := ing.Ingest(context.Background(), scan.ScanSourceRegistry, res, scan.IngestMeta{
		ScanID:     "test-sast",
		ProjectDir: corpus,
		Categories: []api.ScanCategory{api.ScanCategorySAST},
		Scanner:    testScannerContract("lycaon-sast"),
	})
	testutil.FailErr(t, "Ingest", err)
	raw, _ := rec.Artifacts["guidance"].([]api.ScanGuidanceSummary)
	if len(raw) == 0 {
		t.Fatalf("expected guidance rows for %d findings", len(res.Findings))
	}
}

// Scan an isolated source tree, as production does after snapshot publication.
func scanFixtureCorpus(t *testing.T) string {
	t.Helper()
	corpus := t.TempDir()
	source := filepath.Join(configlayout.FindModuleRoot(), "test", "testdata", "scan")
	testutil.FailErr(t, "copy scan corpus", os.CopyFS(corpus, os.DirFS(source)))
	return corpus
}
