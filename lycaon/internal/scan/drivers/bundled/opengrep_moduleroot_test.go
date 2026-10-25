package bundleddriver_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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
func TestOpenGrepScopesExcludesToProjectNotAncestorDirectories(t *testing.T) {
	testutil.SkipIfShort(t, "runs real opengrep over the scan fixture")
	confine.TestingSetAutoConfine(t)
	root := configlayout.FindModuleRoot()
	corpus := filepath.Join(t.TempDir(), "build", "project")
	testutil.FailErr(t, "create project under excluded ancestor", os.MkdirAll(corpus, 0o700))
	source := filepath.Join(root, "test", "testdata", "scan")
	testutil.FailErr(t, "copy scan corpus", os.CopyFS(corpus, os.DirFS(source)))
	testutil.FailErr(t, "create in-project build output", os.MkdirAll(filepath.Join(corpus, "build"), 0o700))
	testutil.FailErr(t, "write excluded build output", os.WriteFile(filepath.Join(corpus, "build", "ignored.rs"), []byte("use reqwest::Client;\nfn main() { Client::builder().danger_accept_invalid_certs(true); }"), 0o600))
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
	for _, path := range res.ScannedPaths {
		path = strings.TrimPrefix(filepath.ToSlash(path), filepath.ToSlash(corpus)+"/")
		if strings.HasPrefix(path, "build/") {
			t.Fatalf("in-project build exclusion was weakened: %s", path)
		}
	}
	if len(res.Findings) == 0 {
		t.Fatalf("findings_count = %d but Findings slice empty (ingest and MCP query need populated findings)", res.FindingsCount)
	}
}
