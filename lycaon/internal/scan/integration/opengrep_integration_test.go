//go:build integration

package integration

import (
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
	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestOpenGrepBundledAdapterFixture(t *testing.T) {
	if testing.Short() {
		t.Skip("opengrep binary integration skipped in -short")
	}
	confine.TestingSetAutoConfine(t)

	manifest, err := bundled.LoadManifest()
	testutil.FailErr(t, "bundled.LoadManifest failed", err)
	home, err := configdir.UserConfigDir()
	testutil.FailErr(t, "configdir.UserConfigDir failed", err)
	if _, err := bundled.ResolveOpenGrepBinary(manifest, home, configlayout.EngineRoot()); err != nil {
		testutil.MissingScannerResource(t, "opengrep", err)
	}

	scanner := bundleddriver.NewOpenGrepScanner(bundleddriver.OpenGrepOptions{
		ID:       "test-sast",
		HomeDir:  home,
		Manifest: manifest,
	})

	fixture := t.TempDir()
	for name, source := range map[string]string{
		"main.rs":        "use reqwest::Client;\nfn main() { let client = Client::builder().danger_accept_invalid_certs(true); }",
		"app.vue":        "<template><div>Hi</div></template>\n<script setup lang=\"ts\">const code: string = location.hash; eval(code);</script>",
		"run.ps1":        "<#\niex ((New-Object Net.WebClient).DownloadString($example))\n#>\niex ((New-Object Net.WebClient).DownloadString($url))\n",
		"data.proto":     "syntax = \"proto3\"; message Example { string name = 1; }",
		"safe.html":      "<!-- <script>eval(location.hash)</script> -->",
		".semgrepignore": "ignored.vue\n",
		"ignored.vue":    "<script>eval(location.hash)</script>",
	} {
		testutil.FailErr(t, "write "+name, os.WriteFile(filepath.Join(fixture, name), []byte(source), 0o600))
	}
	res, err := scanner.Run(t.Context(), scan.ScanRequest{ProjectDir: fixture, Categories: []api.ScanCategory{api.ScanCategorySAST}})
	testutil.FailErr(t, "scan fixture", err)
	found := make(map[string]bool)
	for _, finding := range res.Findings {
		if len(finding.Locations) != 1 {
			t.Fatalf("locations = %+v", finding.Locations)
		}
		loc := finding.Locations[0]
		file := filepath.Base(loc.URI)
		found[file] = true
		if strings.Contains(loc.URI, "embedded") {
			t.Fatalf("projection path escaped: %s", loc.URI)
		}
		if strings.Contains(finding.RuleID, "coverage") {
			t.Fatalf("coverage counted as vulnerability: %s", finding.RuleID)
		}
		if file == "app.vue" && loc.StartLine != 2 {
			t.Fatalf("Vue source line = %d", loc.StartLine)
		}
		if file == "app.vue" {
			if finding.Dataflow == nil || finding.Dataflow.Source == nil || finding.Dataflow.Sink == nil {
				t.Fatal("Vue flow evidence missing")
			}
			scanfindings.VisitFindingLocations(&finding, func(location *api.SecurityFindingLocation) {
				if filepath.Base(location.URI) != "app.vue" || location.StartLine != 2 {
					t.Errorf("unmapped Vue evidence: %+v", location)
				}
			})
		}
		if file == "run.ps1" && loc.StartLine != 4 {
			t.Fatalf("PowerShell source line = %d", loc.StartLine)
		}
	}
	if len(res.Findings) != 3 || !found["main.rs"] || !found["app.vue"] || !found["run.ps1"] {
		t.Fatalf("unexpected findings: %+v", res.Findings)
	}
	if len(res.Warnings) != 0 {
		t.Fatalf("unexpected warnings = %+v", res.Warnings)
	}
}
