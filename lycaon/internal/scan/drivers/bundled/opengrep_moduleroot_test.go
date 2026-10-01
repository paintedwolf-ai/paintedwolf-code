package bundleddriver_test

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
	bundleddriver "github.com/lycaon/lycaon/internal/scan/drivers/bundled"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// Rule paths stay absolute because the subprocess runs from the project root.
func TestOpenGrepUsesAbsoluteRuleConfigsFromProjectWorkingDirectory(t *testing.T) {
	testutil.SkipIfShort(t, "runs real opengrep over the scan fixture")
	confine.TestingSetAutoConfine(t)
	root := configlayout.FindModuleRoot()
	corpus := t.TempDir()
	source := filepath.Join(root, "test", "testdata", "scan")
	testutil.FailErr(t, "copy scan corpus", os.CopyFS(corpus, os.DirFS(source)))
	home, err := configdir.UserConfigDir()
	testutil.FailErr(t, "configdir.UserConfigDir failed", err)
	manifest, err := bundled.LoadManifest()
	testutil.FailErr(t, "bundled.LoadManifest failed", err)
	if _, err := bundled.ResolveOpenGrepBinary(manifest, home, configlayout.EngineRoot()); err != nil {
		testutil.MissingScannerResource(t, "opengrep", err)
	}
	t.Chdir(root)
	sc := bundleddriver.NewOpenGrepScanner(bundleddriver.OpenGrepOptions{
		HomeDir:  home,
		Manifest: manifest,
	})
	res, err := sc.Run(context.Background(), scan.ScanRequest{
		ProjectDir: corpus,
		Categories: []api.ScanCategory{api.ScanCategorySAST},
	})
	testutil.FailErr(t, "sc.Run failed", err)
	if res.FindingsCount < 1 {
		t.Fatalf("findings_count = %d, want >= 1 (relative module root must not break --config paths); scanned=%v, warnings=%+v", res.FindingsCount, res.ScannedPaths, res.Warnings)
	}
	if len(res.Findings) == 0 {
		t.Fatalf("findings_count = %d but Findings slice empty (ingest and MCP query need populated findings)", res.FindingsCount)
	}
}
