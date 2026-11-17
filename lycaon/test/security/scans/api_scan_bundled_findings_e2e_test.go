package scans

import (
	"context"
	"os"
	"testing"

	"github.com/lycaon/lycaon/test/security"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/scan/bundled"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
)

func TestAPIScanBundledScannerFindingsE2E(t *testing.T) {
	testutil.SkipIfRace(t, "bundled scanner subprocesses exceed the integration budget under race instrumentation")
	projectDir := security.ScanFixtureDir(t)
	if _, err := os.Stat(projectDir); err != nil {
		testutil.FailErr(t, "scan fixture dir", err)
	}

	h := wiring.BuildForTest(t, wiring.WithBundledScanners())
	cancel := h.StartBackgroundWorkers(t, context.Background())
	t.Cleanup(cancel)
	srv := h.Server

	cases := []struct {
		name      string
		category  wire.ScanCategory
		wantFile  string
		minCount  int
		skipShort bool
	}{
		{
			name:     "secret_gitleaks",
			category: wire.ScanCategorySecret,
			WantFile: "secrets.env",
			minCount: 1,
		},
		{
			name:      "sast_opengrep",
			category:  wire.ScanCategorySAST,
			WantFile:  "vuln.go",
			minCount:  1,
			skipShort: true,
		},
		{
			name:      "sca_scalibr",
			category:  wire.ScanCategorySCA,
			WantFile:  "package-lock.json",
			minCount:  1,
			skipShort: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.skipShort && testing.Short() {
				t.Skip("bundled scanner network/binary integration skipped in -short")
			}
			if tc.category == wire.ScanCategorySAST {
				skipIfOpenGrepUnavailable(t)
			}
			created := security.EnqueueCodeScan(t, srv, projectDir, []wire.ScanCategory{tc.category})
			got := security.WaitScanComplete(t, srv, created.ProjectID, created.ID)
			security.AssertScanFindings(t, got, tc.wantFile, tc.minCount)
			if tc.category == wire.ScanCategorySCA {
				if _, err := os.Stat(project.OSVCacheDir("")); !os.IsNotExist(err) {
					t.Fatalf("provisioned dependency scan created a mutable OSV refresh cache: %v", err)
				}
			}
		})
	}
}

func skipIfOpenGrepUnavailable(t *testing.T) {
	t.Helper()
	manifest, err := bundled.LoadManifest()
	if err != nil {
		testutil.MissingScannerResource(t, "opengrep manifest", err)
	}
	home, err := configdir.UserConfigDir()
	if err != nil {
		testutil.MissingScannerResource(t, "user config dir", err)
	}
	if _, err := bundled.ResolveOpenGrepBinary(manifest, home, configlayout.EngineRoot()); err != nil {
		testutil.MissingScannerResource(t, "opengrep", err)
	}
}
