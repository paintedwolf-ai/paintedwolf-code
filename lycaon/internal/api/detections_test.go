package api

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/catalogview"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/detectionpack"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// Shipped detection packs arrive as resolved catalog units, so a server with no
// catalog view has no packs at all. This wires the catalog view serve wires;
// without it these tests pass against a device that runs no detections.
func newDetectionsTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	cfgDir := t.TempDir()
	var last *detectionpack.Matcher
	rewire := func(m *detectionpack.Matcher) { last = m }
	srv, _, _ := newSettingsTestServer(t, func(d *Dependencies) {
		d.DataDir = cfgDir
		d.PublishDetections = rewire
	})
	root := configlayout.FindModuleRoot()
	boot := extpacks.Resolve(t.Context(), extpacks.ResolveInput{
		Packs: func() []extpacks.PackContent {
			content, err := extpacks.DiscoverStockContent()
			testutil.FailErr(t, "DiscoverStockContent", err)
			return content
		}(),
		Desired: extpacks.EmptyDesired(),
	})
	srv.sessions.SetEffectiveCatalogDeps(root, boot, nil)
	srv.sessions.Catalog.SetCatalogViewCache(catalogview.NewCache(root, slog.Default()))
	_ = last
	return srv, cfgDir
}

func TestListDetectionPacks(t *testing.T) {
	srv, cfgDir := newDetectionsTestServer(t)
	req := newAuthedRequest(http.MethodGet, "/v1/detection-packs", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var list wire.DetectionPackList
	if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil {
		testutil.FailErr(t, "decode", err)
	}
	if len(list.Packs) < 7 {
		t.Fatalf("packs=%d", len(list.Packs))
	}
	// Hand-edit device folder appears on next GET without restart.
	packDir := filepath.Join(cfgDir, "detection-packs", "hand-edit")
	if err := os.MkdirAll(filepath.Join(packDir, "rules"), 0o755); err != nil {
		testutil.FailErr(t, "mkdir", err)
	}
	manifest := []byte("id: hand-edit\nlabel: Hand\ndescription: d\n")
	if err := os.WriteFile(filepath.Join(packDir, "pack.yaml"), manifest, 0o644); err != nil {
		testutil.FailErr(t, "write pack", err)
	}
	rule := []byte(`title: T
id: 11111111-1111-4111-8111-111111111111
description: d
level: high
logsource: {product: lycaon, service: tool_exec}
detection:
  sel: {Image: handbin}
  condition: sel
`)
	if err := os.WriteFile(filepath.Join(packDir, "rules", "one.yml"), rule, 0o644); err != nil {
		testutil.FailErr(t, "write rule", err)
	}
	req = newAuthedRequest(http.MethodGet, "/v1/detection-packs", nil)
	rr = httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil {
		testutil.FailErr(t, "decode2", err)
	}
	found := false
	for _, p := range list.Packs {
		if p.ID == "hand-edit" && p.Source == "device" {
			found = true
		}
	}
	if !found {
		t.Fatal("hand-edit device pack missing after GET reload")
	}
}

func TestToggleDetectionPack(t *testing.T) {
	srv, _ := newDetectionsTestServer(t)
	body := []byte(`{"enabled":false}`)
	req := newAuthedRequest(http.MethodPatch, "/v1/detection-packs/aws-cli", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var pack wire.DetectionPack
	if err := json.Unmarshal(rr.Body.Bytes(), &pack); err != nil {
		testutil.FailErr(t, "decode", err)
	}
	if pack.Enabled {
		t.Fatal("expected disabled")
	}
	req = newAuthedRequest(http.MethodPatch, "/v1/detection-packs/no-such-pack", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr = httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("unknown pack status=%d", rr.Code)
	}
}

func TestToggleDetectionPackRequiresEnabled(t *testing.T) {
	srv, _ := newDetectionsTestServer(t)
	for _, body := range []string{`{}`, `{"enabled":null}`, `null`} {
		req := newAuthedRequest(http.MethodPatch, "/v1/detection-packs/aws-cli", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("body %s status=%d want 400 body=%s", body, rr.Code, rr.Body.String())
		}
	}
	req := newAuthedRequest(http.MethodGet, "/v1/detection-packs", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	var list wire.DetectionPackList
	if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil {
		testutil.FailErr(t, "decode list", err)
	}
	for _, p := range list.Packs {
		if p.ID == "aws-cli" && !p.Enabled {
			t.Fatal("omitted enabled must not disable the pack")
		}
	}
}

func TestImportDetectionPackDryRunAndCommit(t *testing.T) {
	srv, cfgDir := newDetectionsTestServer(t)
	src := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "rules"), 0o755); err != nil {
		testutil.FailErr(t, "mkdir", err)
	}
	if err := os.WriteFile(filepath.Join(src, "pack.yaml"), []byte("id: import-me\nlabel: Imp\ndescription: d\n"), 0o644); err != nil {
		testutil.FailErr(t, "manifest", err)
	}
	rule := []byte(`title: T
id: 22222222-2222-4222-8222-222222222222
description: d
level: high
logsource: {product: lycaon, service: tool_exec}
detection:
  sel: {Image: importbin}
  condition: sel
`)
	if err := os.WriteFile(filepath.Join(src, "rules", "one.yml"), rule, 0o644); err != nil {
		testutil.FailErr(t, "rule", err)
	}

	dryBody, _ := json.Marshal(wire.DetectionPackImportRequest{Path: src, DryRun: true})
	req := newAuthedRequest(http.MethodPost, "/v1/detection-packs", bytes.NewReader(dryBody))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("dry_run status=%d body=%s", rr.Code, rr.Body.String())
	}
	var dry wire.DetectionPackImportResult
	if err := json.Unmarshal(rr.Body.Bytes(), &dry); err != nil {
		testutil.FailErr(t, "decode dry", err)
	}
	if !dry.DryRun || dry.Pack.ID != "import-me" {
		t.Fatalf("dry=%+v", dry)
	}
	if _, err := os.Stat(filepath.Join(cfgDir, "detection-packs", "import-me")); !os.IsNotExist(err) {
		t.Fatal("dry_run must not write")
	}

	commitBody, _ := json.Marshal(wire.DetectionPackImportRequest{Path: src})
	req = newAuthedRequest(http.MethodPost, "/v1/detection-packs", bytes.NewReader(commitBody))
	req.Header.Set("Content-Type", "application/json")
	rr = httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("commit status=%d body=%s", rr.Code, rr.Body.String())
	}
	req = newAuthedRequest(http.MethodGet, "/v1/detection-packs", nil)
	rr = httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	var list wire.DetectionPackList
	if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil {
		testutil.FailErr(t, "list", err)
	}
	found := false
	for _, p := range list.Packs {
		if p.ID == "import-me" && p.Enabled {
			found = true
		}
	}
	if !found {
		t.Fatal("committed pack missing from GET")
	}
}

func TestImportDetectionPackErrors(t *testing.T) {
	srv, _ := newDetectionsTestServer(t)
	// Bundled id collision
	src := t.TempDir()
	_ = os.MkdirAll(filepath.Join(src, "rules"), 0o755)
	_ = os.WriteFile(filepath.Join(src, "pack.yaml"), []byte("id: aws-cli\nlabel: X\ndescription: d\n"), 0o644)
	_ = os.WriteFile(filepath.Join(src, "rules", "one.yml"), []byte(`title: T
id: 33333333-3333-4333-8333-333333333333
description: d
level: high
logsource: {product: lycaon, service: tool_exec}
detection:
  sel: {Image: x}
  condition: sel
`), 0o644)
	body, _ := json.Marshal(wire.DetectionPackImportRequest{Path: src})
	req := newAuthedRequest(http.MethodPost, "/v1/detection-packs", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusConflict {
		t.Fatalf("bundled collision status=%d body=%s", rr.Code, rr.Body.String())
	}

	body, _ = json.Marshal(wire.DetectionPackImportRequest{Path: "/no/such/path"})
	req = newAuthedRequest(http.MethodPost, "/v1/detection-packs", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr = httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("missing path status=%d", rr.Code)
	}

	empty := t.TempDir()
	body, _ = json.Marshal(wire.DetectionPackImportRequest{Path: empty})
	req = newAuthedRequest(http.MethodPost, "/v1/detection-packs", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr = httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("no pack.yaml status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestDeleteDetectionPack(t *testing.T) {
	srv, cfgDir := newDetectionsTestServer(t)
	// Seed a device pack via import commit.
	src := t.TempDir()
	_ = os.MkdirAll(filepath.Join(src, "rules"), 0o755)
	_ = os.WriteFile(filepath.Join(src, "pack.yaml"), []byte("id: del-me\nlabel: D\ndescription: d\n"), 0o644)
	_ = os.WriteFile(filepath.Join(src, "rules", "one.yml"), []byte(`title: T
id: 44444444-4444-4444-8444-444444444444
description: d
level: high
logsource: {product: lycaon, service: tool_exec}
detection:
  sel: {Image: delbin}
  condition: sel
`), 0o644)
	body, _ := json.Marshal(wire.DetectionPackImportRequest{Path: src})
	req := newAuthedRequest(http.MethodPost, "/v1/detection-packs", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("import status=%d body=%s", rr.Code, rr.Body.String())
	}

	req = newAuthedRequest(http.MethodDelete, "/v1/detection-packs/del-me", nil)
	rr = httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("delete status=%d", rr.Code)
	}
	if _, err := os.Stat(filepath.Join(cfgDir, "detection-packs", "del-me")); !os.IsNotExist(err) {
		t.Fatal("device pack still on disk")
	}

	req = newAuthedRequest(http.MethodDelete, "/v1/detection-packs/aws-cli", nil)
	rr = httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusConflict {
		t.Fatalf("bundled delete status=%d", rr.Code)
	}
}
