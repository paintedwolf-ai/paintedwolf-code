//go:build integration

package security

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/lycaon/lycaon/internal/scan/bundled"
	"github.com/lycaon/lycaon/internal/scan/opengrep"
	"github.com/lycaon/lycaon/internal/scan/rules"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
)

type ordinaryScanDiagnostic struct {
	File      string               `yaml:"file"`
	Kind      wire.ScanWarningKind `yaml:"kind"`
	Construct string               `yaml:"construct"`
	Line      int                  `yaml:"line"`
	Column    int                  `yaml:"column"`
}

type ordinaryScanProject struct {
	Name        string                                     `yaml:"name"`
	Files       map[string]string                          `yaml:"files"`
	Diagnostics map[opengrep.Mode][]ordinaryScanDiagnostic `yaml:"diagnostics"`
	Provenance  struct {
		SourceSHA256 map[string]string `yaml:"source_sha256"`
	} `yaml:"provenance"`
}

func loadOrdinaryScanProject(t *testing.T) ordinaryScanProject {
	t.Helper()
	path := filepath.Join(scanFixtureDir(t), "..", "opengrep-projects", "ordinary-javascript.yaml")
	raw, err := os.ReadFile(path)
	testutil.FailErr(t, "read ordinary project corpus", err)
	var corpus struct {
		Projects []ordinaryScanProject `yaml:"projects"`
	}
	testutil.FailErr(t, "decode ordinary project corpus", yaml.Unmarshal(raw, &corpus))
	for _, project := range corpus.Projects {
		if project.Name == "ordinary-javascript-hackathon-starter" {
			if len(project.Files) == 0 || len(project.Diagnostics) != 2 {
				t.Fatal("ordinary project must include source and diagnostic expectations for both analysis modes")
			}
			return project
		}
	}
	t.Fatal("ordinary project fixture is missing")
	return ordinaryScanProject{}
}

func ordinaryScanExpectedDiagnostics(t *testing.T, fixture ordinaryScanProject) []ordinaryScanDiagnostic {
	t.Helper()
	gates, err := rules.LoadOpengrepGates()
	testutil.FailErr(t, "load ordinary scan analysis policy", err)
	expected, exists := fixture.Diagnostics[gates.Analysis.Mode]
	if !exists || len(expected) == 0 {
		t.Fatalf("ordinary project lacks partial-analysis expectations for %s", gates.Analysis.Mode)
	}
	return expected
}

func materializeOrdinaryScanProject(t *testing.T, fixture ordinaryScanProject) string {
	t.Helper()
	project := t.TempDir()
	for path, source := range fixture.Files {
		if !filepath.IsLocal(path) || filepath.ToSlash(filepath.Clean(path)) != path {
			t.Fatalf("invalid ordinary project path %q", path)
		}
		if got := fmt.Sprintf("%x", sha256.Sum256([]byte(source))); got != fixture.Provenance.SourceSHA256[path] {
			t.Fatalf("ordinary source %s differs from its reviewed digest", path)
		}
		target := filepath.Join(project, path)
		testutil.FailErr(t, "create ordinary project directory", os.MkdirAll(filepath.Dir(target), 0o700))
		testutil.FailErr(t, "write ordinary project source", os.WriteFile(target, []byte(source), 0o600))
	}
	return project
}

func assertOrdinaryProjectScan(t *testing.T, h *wiring.Harness, manifest *bundled.Manifest, binary string) {
	t.Helper()
	fixture := loadOrdinaryScanProject(t)
	expected := ordinaryScanExpectedDiagnostics(t, fixture)
	project := materializeOrdinaryScanProject(t, fixture)
	created := enqueueCodeScan(t, h.Server, project, []wire.ScanCategory{wire.ScanCategorySAST})
	got := waitScanComplete(t, h.Server, created.ProjectID, created.ID, bundledScannerWaitBudget)
	if got.Status != wire.CodeScanStatusComplete || got.CoverageStatus != wire.ScanCoveragePartial {
		t.Fatalf("ordinary scan status=%s coverage=%s error=%s", got.Status, got.CoverageStatus, got.Error)
	}
	if got.FindingsCount != 0 || len(got.Findings) != 0 {
		t.Fatalf("ordinary project emitted unexpected findings: count=%d findings=%+v", got.FindingsCount, got.Findings)
	}
	assertMaintainedExecutionIdentity(t, manifest, binary, got)
	assertOrdinaryScanDiagnostics(t, expected, got.Warnings)
	recorder := httptest.NewRecorder()
	h.Server.ServeHTTP(recorder, authedRequest(t, http.MethodGet, scanURL(created.ProjectID, created.ID), nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("ordinary summary status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var summary wire.CodeScan
	testutil.FailErr(t, "decode ordinary scan summary", json.Unmarshal(recorder.Body.Bytes(), &summary))
	if summary.Status != wire.CodeScanStatusComplete || summary.CoverageStatus != wire.ScanCoveragePartial ||
		summary.FindingsCount != 0 || len(summary.Warnings) != 0 {
		t.Fatalf("ordinary summary lost coverage or exposed detailed warnings: %+v", summary)
	}
	if len(summary.WarningSummary) != 1 || summary.WarningSummary[0].Kind != wire.ScanWarningFilePartialSemantics ||
		summary.WarningSummary[0].Count != len(expected) {
		t.Fatalf("ordinary summary did not preserve diagnostic count in one group: %+v", summary.WarningSummary)
	}
}

func assertOrdinaryScanDiagnostics(t *testing.T, expected []ordinaryScanDiagnostic, warnings []wire.ScanWarning) {
	t.Helper()
	want := make(map[ordinaryScanDiagnostic]int)
	for _, diagnostic := range expected {
		want[diagnostic]++
	}
	got := make(map[ordinaryScanDiagnostic]int)
	for _, warning := range warnings {
		got[ordinaryScanDiagnostic{
			File: warning.File, Kind: warning.Kind, Construct: warning.Construct,
			Line: warning.StartLine, Column: warning.StartColumn,
		}]++
	}
	for diagnostic, count := range want {
		if got[diagnostic] != count {
			t.Errorf("ordinary diagnostic %+v count=%d; want %d", diagnostic, got[diagnostic], count)
		}
		delete(got, diagnostic)
	}
	for diagnostic, count := range got {
		t.Errorf("unexpected ordinary diagnostic %+v count=%d", diagnostic, count)
	}
}
