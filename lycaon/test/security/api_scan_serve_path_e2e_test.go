package security

import (
	"context"
	"github.com/lycaon/lycaon/internal/configlayout"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/app"
	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/mcp"
	"github.com/lycaon/lycaon/internal/scan/bundled"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestAPIScanServePathSASTE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("serve-path bundled SAST E2E skipped in -short")
	}

	moduleRoot := configlayout.FindModuleRoot()
	manifest, err := bundled.LoadManifest()
	if err != nil {
		t.Fatalf("opengrep manifest: %v", err)
	}
	home, err := configdir.UserConfigDir()
	testutil.FailErr(t, "UserConfigDir", err)
	if _, err := bundled.ResolveOpenGrepBinary(manifest, home, configlayout.EngineRoot()); err != nil {
		testutil.MissingScannerResource(t, "opengrep", err)
	}

	projectDir := externalScanProject(t)
	t.Chdir(moduleRoot)

	t.Setenv("LYCAON_LLM_MOCK", "1")
	t.Setenv("LYCAON_API_TOKEN", api.TestAPIToken)
	t.Setenv("LYCAON_TEST", "")

	cfg := app.DefaultConfig()
	cfg.DBPath = filepath.Join(t.TempDir(), "serve-path-scan.db")
	cfg.ListenAddr = "127.0.0.1:0"
	cfg.TestMCPConnector = &mcp.MockConnector{Tools: map[string][]*sdkmcp.Tool{
		"svca": {{Name: "do", Description: "do"}},
	}}
	cfg.TestMCPGlobalOverridePath = filepath.Join(t.TempDir(), "mcp-override.yaml")

	serveApp, err := app.Build(t.Context(), cfg)
	testutil.FailErr(t, "app.Build serve path", err)
	t.Cleanup(func() { _ = serveApp.Close() })
	if !filepath.IsAbs(serveApp.ConfigRoot) {
		t.Fatalf("ConfigRoot = %q, want absolute path after ResolveModuleRoot", serveApp.ConfigRoot)
	}

	cancel, err := serveApp.StartBackgroundWorkers(context.Background())
	testutil.FailErr(t, "start background workers", err)
	t.Cleanup(cancel)

	created := enqueueCodeScan(t, serveApp.Server, projectDir, []wire.ScanCategory{wire.ScanCategorySAST})
	got := waitScanComplete(t, serveApp.Server, created.ProjectID, created.ID, 90*time.Second)
	if got.ScannerID != "lycaon-sast" {
		t.Fatalf("scanner_id = %q, want lycaon-sast", got.ScannerID)
	}
	assertScanFindings(t, got, "vuln.go", 1)
}

func externalScanProject(t *testing.T) string {
	t.Helper()
	moduleRoot := configlayout.FindModuleRoot()
	parent := filepath.Dir(moduleRoot)
	dir, err := os.MkdirTemp(parent, "lycaon-scan-serve-path-*") //nolint:usetesting // external to module dir; t.TempDir lives under module root
	testutil.FailErr(t, "MkdirTemp outside module", err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	src := filepath.Join(scanFixtureDir(t), "vuln.go")
	dst := filepath.Join(dir, "vuln.go")
	in, err := os.Open(src)
	testutil.FailErr(t, "open vuln.go fixture", err)
	defer in.Close()
	out, err := os.Create(dst)
	testutil.FailErr(t, "create external vuln.go", err)
	_, err = io.Copy(out, in)
	_ = out.Close()
	testutil.FailErr(t, "copy vuln.go", err)
	return dir
}
