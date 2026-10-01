package contract

import (
	"net/http"
	"path/filepath"
	"testing"

	scancatalog "github.com/lycaon/lycaon/internal/scan/catalog"
	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestExternalScannerYAMLExampleValidates(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	moduleRoot := filepath.Join(root, "lycaon")
	path := filepath.Join(moduleRoot, "internal", "scan", "testdata", "project-scanners.yaml")
	overlay, err := scancatalog.LoadUserScannerOverlay(path)
	contractcheck.FailErr(t, "scan.LoadUserScannerOverlay failed", err)

	host, err := scancatalog.LoadScannerConfig()
	contractcheck.FailErr(t, "load bundled host catalog", err)
	entries, rejected, err := scancatalog.MergeScannerCatalog(host, nil, overlay, scancatalog.MergeOptions{})
	contractcheck.FailErr(t, "scan.MergeScannerCatalog failed", err)
	if len(rejected) != 0 {
		t.Fatalf("project overlay produced rejected rows: %+v", rejected)
	}
	merged := &scancatalog.ScannerConfig{Scanners: entries}
	contractcheck.FailErr(t, "scan.ValidateScannerConfig failed", scancatalog.ValidateScannerConfig(merged))

	for _, s := range merged.Scanners {
		if s.Driver == scancatalog.DriverExternal && !scanoutput.IsRegisteredOutputParser(s.OutputParser) {
			t.Fatalf("unknown parser %q for %q", s.OutputParser, s.ID)
		}
	}
}

func TestListScannersOpenAPIRoute(t *testing.T) {
	t.Parallel()
	srv := NewStubServer()
	t.Cleanup(srv.Close)
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+"/v1/scanners", nil)
	resp, err := srv.Client().Do(req)
	contractcheck.FailErr(t, "srv.Client failed", err)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}
