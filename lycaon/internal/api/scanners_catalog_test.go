package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/project"
	scancatalog "github.com/lycaon/lycaon/internal/scan/catalog"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// withScannerCatalog resolves bundled scanners from the module checkout.
func withScannerCatalog(d *Dependencies) { d.ModuleRoot = configlayout.FindModuleRoot() }

func TestScannersCRUDCreateUserExternal(t *testing.T) {
	t.Setenv("LYCAON_TEST", "1")
	home := t.TempDir()
	t.Setenv(configdir.EnvConfigDir, home)
	srv := newTestServer(t, withScannerCatalog)

	runtimePolicy := wire.ScanRuntimePolicy{SoftLimitMs: 900000, HardLimitMs: 0, CPUUnits: 1, Parallelism: 1}
	body, err := json.Marshal(wire.CreateCustomScannerRequest{
		Source:       "custom",
		ID:           "fake-ext",
		Engine:       "fake-ext",
		ScopeKind:    string(scancatalog.ScopeSourceDriver),
		Command:      []string{"true", scancatalog.ArgTokenScanTarget},
		OutputParser: "sarif",
		Categories:   []string{"sast"},
		Runtime:      &runtimePolicy,
	})
	testutil.FailErr(t, "marshal create", err)

	req := newAuthedRequest(http.MethodPost, "/v1/scanners", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d body=%s", rec.Code, rec.Body.String())
	}
	var created wire.ScannerSummary
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		testutil.FailErr(t, "decode create", err)
	}
	if created.ID != "fake-ext" || created.Driver != "external" || created.Enabled {
		t.Fatalf("created = %+v", created)
	}
	if created.CatalogSource != "user" {
		t.Fatalf("catalog_source = %q", created.CatalogSource)
	}

	listReq := newAuthedRequest(http.MethodGet, "/v1/scanners", nil)
	listRec := httptest.NewRecorder()
	srv.ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("list status = %d body=%s", listRec.Code, listRec.Body.String())
	}
	var list wire.ScannerListResponse
	if err := json.Unmarshal(listRec.Body.Bytes(), &list); err != nil {
		testutil.FailErr(t, "decode list", err)
	}
	found := false
	for _, s := range list.Scanners {
		if s.ID == "fake-ext" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("list missing fake-ext: %+v", list.Scanners)
	}
	data, err := os.ReadFile(filepath.Join(home, "scanners.yaml"))
	testutil.FailErr(t, "read user scanners.yaml", err)
	if !bytes.Contains(data, []byte("fake-ext")) {
		t.Fatalf("user yaml missing id: %s", data)
	}
}

func TestScannersCreateRejectsInvalidRuntime(t *testing.T) {
	t.Setenv("LYCAON_TEST", "1")
	t.Setenv(configdir.EnvConfigDir, t.TempDir())
	srv := newTestServer(t, withScannerCatalog)

	runtimePolicy := wire.ScanRuntimePolicy{SoftLimitMs: 900000, HardLimitMs: 60000, CPUUnits: 1, Parallelism: 1}
	body, err := json.Marshal(wire.CreateCustomScannerRequest{
		Source: "custom",
		ID:     "invalid-runtime", Engine: "invalid-runtime", ScopeKind: string(scancatalog.ScopeSourceDriver),
		Command: []string{"true", scancatalog.ArgTokenScanTarget}, OutputParser: "sarif",
		Categories: []string{"sast"}, Runtime: &runtimePolicy,
	})
	testutil.FailErr(t, "marshal create", err)

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, newAuthedRequest(http.MethodPost, "/v1/scanners", bytes.NewReader(body)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("create status = %d want 400 body=%s", rec.Code, rec.Body.String())
	}
}

func TestScannersProjectEnableOnly(t *testing.T) {
	t.Setenv("LYCAON_TEST", "1")
	t.Setenv(configdir.EnvConfigDir, t.TempDir())
	srv := newTestServer(t, withScannerCatalog, withTrustSurfaces(t))

	projectDir := t.TempDir()
	p, err := project.CreateWithRoot(t.Context(), srv.projectRegistry, projectDir)
	testutil.FailErr(t, "CreateWithRoot", err)

	listReq := newAuthedRequest(http.MethodGet, "/v1/scanners", nil)
	listRec := httptest.NewRecorder()
	srv.ServeHTTP(listRec, listReq)
	var list wire.ScannerListResponse
	testutil.FailErr(t, "decode list", json.Unmarshal(listRec.Body.Bytes(), &list))
	if len(list.Scanners) == 0 {
		t.Fatal("expected host scanners")
	}
	hostID := list.Scanners[0].ID

	body, err := json.Marshal(wire.UpdateProjectScannerRequest{Enabled: new(false)})
	testutil.FailErr(t, "marshal patch", err)
	putReq := newAuthedRequest(http.MethodPatch, "/v1/projects/"+p.ID+"/scanners/"+hostID, bytes.NewReader(body))
	putRec := httptest.NewRecorder()
	srv.ServeHTTP(putRec, putReq)
	if putRec.Code != http.StatusOK {
		t.Fatalf("project enable status = %d body=%s", putRec.Code, putRec.Body.String())
	}
	var sum wire.ScannerSummary
	testutil.FailErr(t, "decode put", json.Unmarshal(putRec.Body.Bytes(), &sum))
	if sum.Enabled {
		t.Fatalf("expected disabled, got %+v", sum)
	}
	if sum.CatalogSource != "project" {
		t.Fatalf("catalog_source = %q", sum.CatalogSource)
	}

	overlay, err := os.ReadFile(filepath.Join(projectDir, settingsoverlay.DirName(), "scanners.yaml"))
	testutil.FailErr(t, "read project overlay", err)
	if bytes.Contains(overlay, []byte("command:")) {
		t.Fatalf("project overlay must be enable-only: %s", overlay)
	}
}

func TestScannersProjectCommandRejected(t *testing.T) {
	t.Setenv("LYCAON_TEST", "1")
	t.Setenv(configdir.EnvConfigDir, t.TempDir())
	srv := newTestServer(t, withScannerCatalog, withTrustSurfaces(t))

	projectDir := t.TempDir()
	overlayDir := filepath.Join(projectDir, settingsoverlay.DirName())
	testutil.FailErr(t, "mkdir overlay", os.MkdirAll(overlayDir, 0o700))
	bad := []byte("scanners:\n  - id: lycaon-sast\n    command: [evil]\n")
	testutil.FailErr(t, "write bad overlay", os.WriteFile(filepath.Join(overlayDir, "scanners.yaml"), bad, 0o600))

	p, err := project.CreateWithRoot(t.Context(), srv.projectRegistry, projectDir)
	testutil.FailErr(t, "CreateWithRoot", err)

	listReq := newAuthedRequest(http.MethodGet, "/v1/projects/"+p.ID+"/scanners", nil)
	listRec := httptest.NewRecorder()
	srv.ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("list status = %d body=%s", listRec.Code, listRec.Body.String())
	}
	var list wire.ScannerListResponse
	testutil.FailErr(t, "decode list", json.Unmarshal(listRec.Body.Bytes(), &list))
	found := false
	for _, row := range list.Rejected {
		if row.Code == "project_fields_forbidden" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected project_fields_forbidden in rejected scanners: %+v", list.Rejected)
	}
}
