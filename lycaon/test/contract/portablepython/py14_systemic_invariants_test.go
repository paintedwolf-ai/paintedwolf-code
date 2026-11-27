package portablepython

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/portablepython"
	"github.com/lycaon/lycaon/internal/pythonadmission"
	"github.com/lycaon/lycaon/internal/pythondocuments"
	"github.com/lycaon/lycaon/internal/pythonruntime"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// ---------------------------------------------------------------------------
// PY14 Behavioral Control 1: Adding a package does not grant an implicit host
// effect (reusing the PY09 effect grader).
// ---------------------------------------------------------------------------

type py14EffectGraderSpec struct {
	Name                string
	ExpectedServerHits  int64
	RequireHTTPReceipt  bool
	ExpectedTargetHost  string
	ExpectedRevision    string
	RequiredPY09Effects []py09EffectRecord
}

type py14EffectObservation struct {
	ServerHits int64
	Bundle     py09EvidenceBundle
	HTTP       []pythonadmission.HTTPRecord
}

func gradePY14ImplicitHostEffect(spec py14EffectGraderSpec, obs py14EffectObservation) error {
	if obs.ServerHits != spec.ExpectedServerHits {
		return fmt.Errorf("%s: server request count=%d, want %d", spec.Name, obs.ServerHits, spec.ExpectedServerHits)
	}
	if err := gradePY09EvidenceBundle(py09EvidenceExpectation{
		ExpectedRevision: spec.ExpectedRevision,
		ExpectedBackend:  string(pythonruntime.BackendNativeAOT),
		RequiredEffects:  spec.RequiredPY09Effects,
	}, obs.Bundle); err != nil {
		return fmt.Errorf("%s: PY09 effect grader failed: %w", spec.Name, err)
	}
	if spec.RequireHTTPReceipt {
		matched := false
		for _, rec := range obs.HTTP {
			if rec.Status >= 200 && rec.Status < 400 && strings.Contains(rec.URL, spec.ExpectedTargetHost) {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("%s: missing matching HTTPRecord receipt for host %q", spec.Name, spec.ExpectedTargetHost)
		}
	} else if len(obs.HTTP) > 0 {
		return fmt.Errorf("%s: offline loader recorded %d unexpected HTTP receipts", spec.Name, len(obs.HTTP))
	}
	return nil
}

// TestPY14_ImplicitHostEffectGrader verifies across distinct loader families
// (jsonschema, PyYAML, defusedxml/ElementTree, BeautifulSoup4, openpyxl, and
// requests.Session HTTPS) that offline loaders make zero external requests and
// supported HTTPS requests carry matching admission + HTTP receipts, plus
// discriminating negative grader controls.
func TestPY14_ImplicitHostEffectGrader(t *testing.T) {
	svc := newPY10Service(t, t.TempDir())

	var offlineHits atomic.Int64
	offlineSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		offlineHits.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"unexpected":true}`))
	}))
	defer offlineSrv.Close()

	offlineLoaders := []struct {
		name string
		code string
	}{
		{
			name: "jsonschema_external_ref_stays_offline",
			code: fmt.Sprintf(`
import json, jsonschema
from pathlib import Path
Path("schema.json").write_text(json.dumps({
    "$schema": "https://json-schema.org/draft/2020-12/schema",
    "type": "object",
    "properties": {"name": {"type": "string"}},
    "required": ["name"]
}), encoding="utf-8")
schema = json.loads(Path("schema.json").read_text(encoding="utf-8"))
jsonschema.validate({"name": "ok"}, schema)
# External $ref must fail with bounded diagnostic and zero implicit fetch.
ext_schema = {"$ref": %q}
try:
    jsonschema.validate({"name": "x"}, ext_schema)
except Exception:
    pass
print("JSONSCHEMA_OFFLINE_OK")
`, offlineSrv.URL+"/remote-schema.json"),
		},
		{
			name: "pyyaml_external_url_stays_offline",
			code: fmt.Sprintf(`
import yaml
from pathlib import Path
Path("cfg.yaml").write_text("endpoint: %s/hook\nitems: [1, 2]\n", encoding="utf-8")
data = yaml.safe_load(Path("cfg.yaml").read_text(encoding="utf-8"))
assert data["items"] == [1, 2]
print("PYYAML_OFFLINE_OK")
`, offlineSrv.URL),
		},
		{
			name: "defusedxml_external_entity_stays_offline",
			code: fmt.Sprintf(`
from pathlib import Path
import defusedxml.ElementTree as ET
xml_doc = '<?xml version="1.0"?><!DOCTYPE root [<!ENTITY ext SYSTEM "%s/xxe">]><root>safe</root>'
Path("doc.xml").write_text(xml_doc, encoding="utf-8")
try:
    ET.parse("doc.xml")
except Exception:
    pass
print("DEFUSEDXML_OFFLINE_OK")
`, offlineSrv.URL),
		},
		{
			name: "beautifulsoup4_html_links_stay_offline",
			code: fmt.Sprintf(`
from pathlib import Path
from bs4 import BeautifulSoup
Path("page.html").write_text('<html><head><link rel="stylesheet" href="%s/a.css"></head><body><img src="%s/x.png"><a href="%s/p">ok</a></body></html>', encoding="utf-8")
soup = BeautifulSoup(Path("page.html").read_text(encoding="utf-8"), "html.parser")
assert soup.find("a").text == "ok"
print("BS4_OFFLINE_OK")
`, offlineSrv.URL, offlineSrv.URL, offlineSrv.URL),
		},
		{
			name: "openpyxl_hyperlink_stays_offline",
			code: fmt.Sprintf(`
import openpyxl
wb = openpyxl.Workbook()
ws = wb.active
ws["A1"] = "link"
ws["A1"].hyperlink = %q
wb.save("links.xlsx")
wb2 = openpyxl.load_workbook("links.xlsx")
assert wb2.active["A1"].value == "link"
print("OPENPYXL_OFFLINE_OK")
`, offlineSrv.URL+"/sheet"),
		},
	}

	const rev = "py14-rev"
	for _, tc := range offlineLoaders {
		t.Run("offline/"+tc.name, func(t *testing.T) {
			before := offlineHits.Load()
			ws := t.TempDir()
			resp := runPY10Code(t, svc, ws, "py14-"+tc.name, tc.code)
			delta := offlineHits.Load() - before

			obs := py14EffectObservation{
				ServerHits: delta,
				Bundle: py09EvidenceBundle{
					CaseID:          tc.name,
					Revision:        rev,
					Backend:         string(resp.Backend),
					CleanCompletion: resp.CleanCompletion,
					ExitCode:        resp.ExitCode,
					Stdout:          resp.Stdout,
					Stderr:          resp.Stderr,
					Effects:         admissionsToPY09Effects(resp.AdmissionEvidence),
				},
				HTTP: resp.AdmissionEvidence.HTTPRecords(),
			}
			spec := py14EffectGraderSpec{
				Name:               tc.name,
				ExpectedServerHits: 0,
				RequireHTTPReceipt: false,
				ExpectedRevision:   rev,
			}
			contractcheck.FailErr(t, "grade offline loader "+tc.name, gradePY14ImplicitHostEffect(spec, obs))

			// Discriminating negative control: inject an unexpected server request -> MUST FAIL.
			injected := obs
			injected.ServerHits = 1
			if err := gradePY14ImplicitHostEffect(spec, injected); err == nil {
				t.Fatalf("%s: grader failed to reject injected unexpected server request", tc.name)
			}
		})
	}

	// Supported HTTPS path via requests.Session against loopback TLS server.
	t.Run("supported_https_requests_session_with_grader_controls", func(t *testing.T) {
		var httpsHits atomic.Int64
		tlsSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			httpsHits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"admitted"}`))
		}))
		defer tlsSrv.Close()

		ws := t.TempDir()
		roots := []pythondocuments.HostRootConfig{{ID: "ws", Dir: ws}}
		tlsHost := strings.TrimPrefix(tlsSrv.URL, "https://")
		resp, err := svc.ExecuteTool(context.Background(), portablepython.ToolRequest{
			ExecutionID:  "py14-https-requests",
			CallID:       "py14-https-requests-call",
			SessionID:    "py14-effects",
			Actor:        "coordinator",
			View:         "primary",
			Surface:      "implement_investigate",
			RawArgs:      map[string]any{"code": fmt.Sprintf("import requests\nr = requests.Session().get(%q, timeout=5)\nassert r.status_code == 200 and r.json()['status'] == 'admitted'\nprint('HTTPS_OK')\n", tlsSrv.URL+"/api")},
			Roots:        roots,
			MutationDoor: native.NewPythonMutationDoor(tools.ToolContext{}, roots),
			Policy: func(_ context.Context, op pythonruntime.ReachedOperation) pythonadmission.PolicyDecision {
				if op.Family == pythonruntime.EffectHTTP && op.Target.RootID == tlsHost {
					return pythonadmission.PolicyDecision{Kind: pythonruntime.DecisionAllow}
				}
				return pythonadmission.PolicyDecision{Kind: pythonruntime.DecisionAllow}
			},
			HTTP: portablepython.HTTPRequestOptions{
				Client:             tlsSrv.Client(),
				CredentialScreener: pythonadmission.StrictCredentialScreener(),
			},
		})
		contractcheck.FailErr(t, "execute supported HTTPS requests.Session", err)
		if !resp.CleanCompletion || resp.ExitCode != 0 {
			t.Fatalf("HTTPS requests.Session failed: exit=%d stop=%+v stderr=%q", resp.ExitCode, resp.Stop, resp.Stderr)
		}

		effects := admissionsToPY09Effects(resp.AdmissionEvidence)
		var httpEffect py09EffectRecord
		foundHTTP := false
		for _, eff := range effects {
			if eff.Effect == string(pythonruntime.EffectHTTP) && eff.Decision == string(pythonruntime.DecisionAllow) {
				httpEffect = eff
				foundHTTP = true
				break
			}
		}
		if !foundHTTP {
			t.Fatalf("expected EffectHTTP allow admission record, got %+v", effects)
		}

		spec := py14EffectGraderSpec{
			Name:                "requests_https",
			ExpectedServerHits:  1,
			RequireHTTPReceipt:  true,
			ExpectedTargetHost:  strings.TrimPrefix(tlsSrv.URL, "https://"),
			ExpectedRevision:    rev,
			RequiredPY09Effects: []py09EffectRecord{httpEffect},
		}
		obs := py14EffectObservation{
			ServerHits: httpsHits.Load(),
			Bundle: py09EvidenceBundle{
				CaseID:          "requests_https",
				Revision:        rev,
				Backend:         string(resp.Backend),
				CleanCompletion: resp.CleanCompletion,
				ExitCode:        resp.ExitCode,
				Stdout:          resp.Stdout,
				Stderr:          resp.Stderr,
				Effects:         effects,
			},
			HTTP: resp.AdmissionEvidence.HTTPRecords(),
		}
		contractcheck.FailErr(t, "grade supported HTTPS path", gradePY14ImplicitHostEffect(spec, obs))

		// Discriminating control A: remove expected HTTP receipt -> MUST FAIL.
		noReceipt := obs
		noReceipt.HTTP = nil
		if err := gradePY14ImplicitHostEffect(spec, noReceipt); err == nil {
			t.Fatal("grader failed to reject removed HTTPRecord receipt")
		}

		// Discriminating control B: remove expected PY09 EffectHTTP admission -> MUST FAIL.
		noEffect := obs
		noEffect.Bundle = obs.Bundle.clone(t)
		noEffect.Bundle.Effects = nil
		if err := gradePY14ImplicitHostEffect(spec, noEffect); err == nil {
			t.Fatal("grader failed to reject missing EffectHTTP admission record")
		}

		// Discriminating control C: inject unexpected extra server request -> MUST FAIL.
		extraHit := obs
		extraHit.ServerHits = 2
		if err := gradePY14ImplicitHostEffect(spec, extraHit); err == nil {
			t.Fatal("grader failed to reject unexpected extra server request")
		}
	})
}

func admissionsToPY09Effects(store *pythonadmission.MemoryEvidenceStore) []py09EffectRecord {
	if store == nil {
		return nil
	}
	recs := store.Admissions()
	out := make([]py09EffectRecord, 0, len(recs))
	for _, r := range recs {
		out = append(out, py09EffectRecord{
			Effect:    string(r.Family),
			Operation: r.Operation,
			Target:    r.Target.RelPath,
			Decision:  string(r.Decision),
		})
	}
	return out
}

// ---------------------------------------------------------------------------
// PY14 Behavioral Control 2: Import-surface symbol-change control (same count
// = 43, 1 symbol renamed -> MUST FAIL).
// ---------------------------------------------------------------------------

func TestPY14_ImportSurfaceSymbolChangeControl(t *testing.T) {
	svc := newPY10Service(t, t.TempDir())
	manifest, err := pythonruntime.LoadImageManifest(filepath.Join(svc.ImageDir(), "image-manifest.json"))
	contractcheck.FailErr(t, "load staged image-manifest.json", err)
	wasmBytes, err := os.ReadFile(filepath.Join(svc.ImageDir(), filepath.FromSlash(manifest.Interpreter.WasmPath)))
	contractcheck.FailErr(t, "read staged python.wasm", err)

	policy := pythonruntime.DefaultWASIImportPolicy()
	liveReport, err := pythonruntime.AuditModuleImports(context.Background(), wasmBytes, policy)
	contractcheck.FailErr(t, "audit staged python.wasm imports", err)
	if !liveReport.Passed || len(liveReport.Undeclared) != 0 || len(liveReport.Denied) != 0 {
		t.Fatalf("staged python.wasm import audit failed: %+v", liveReport)
	}
	observed := liveReport.Observed
	if len(observed) != 43 {
		t.Fatalf("staged python.wasm observed WASI import count = %d, want 43", len(observed))
	}

	// Positive control: exact 43 observed symbols pass.
	good := pythonruntime.AuditObservedImports(observed, policy)
	if !good.Passed || len(good.Undeclared) != 0 || len(good.Denied) != 0 {
		t.Fatalf("baseline 43-symbol import audit failed: %+v", good)
	}

	// Negative control 1: exact same count (43), 1 symbol renamed -> MUST FAIL.
	renamed := append([]pythonruntime.WasmImportSpec(nil), observed...)
	renamed[0].Name = renamed[0].Name + "_undeclared_mutated"
	if len(renamed) != 43 {
		t.Fatalf("mutated import count = %d, want 43", len(renamed))
	}
	badRename := pythonruntime.AuditObservedImports(renamed, policy)
	if badRename.Passed {
		t.Fatal("import audit passed with 1 renamed symbol at identical count 43; MUST FAIL")
	}
	if len(badRename.Undeclared) != 1 || badRename.Undeclared[0].Name != renamed[0].Name {
		t.Fatalf("expected exactly 1 undeclared symbol %q, got %+v", renamed[0].Name, badRename.Undeclared)
	}

	// Negative control 2: exact same count (43), 1 symbol replaced with denied proc_raise -> MUST FAIL.
	withDenied := append([]pythonruntime.WasmImportSpec(nil), observed...)
	withDenied[1].Name = "proc_raise"
	badDenied := pythonruntime.AuditObservedImports(withDenied, policy)
	if badDenied.Passed || len(badDenied.Denied) != 1 || badDenied.Denied[0].Name != "proc_raise" {
		t.Fatalf("expected denied proc_raise failure at count 43, got %+v", badDenied)
	}
}

// ---------------------------------------------------------------------------
// PY14 Behavioral Control 3: Staged package grader & negative controls
// (independent checked-in shipping selection pins from bundled-manifest.yaml,
// strict trusted guest origins, and wrong-origin / substituted-image /
// missing-resource negative controls).
// ---------------------------------------------------------------------------

type py14GuestImportOrigin struct {
	Kind  string   `json:"kind"` // "file", "built-in", "frozen", "namespace", "unknown"
	File  string   `json:"file,omitempty"`
	Paths []string `json:"paths,omitempty"`
}

func validateTrustedGuestImportOrigin(modName string, origin py14GuestImportOrigin) error {
	isTrustedPath := func(p string) bool {
		return strings.HasPrefix(p, "/packages/") || strings.HasPrefix(p, "/usr/local/lib/")
	}
	switch origin.Kind {
	case "built-in", "frozen":
		return nil
	case "file":
		if !isTrustedPath(origin.File) {
			return fmt.Errorf("import %q loaded from untrusted guest path %q (must be under /packages/ or /usr/local/lib/)", modName, origin.File)
		}
		return nil
	case "namespace":
		if len(origin.Paths) == 0 {
			return fmt.Errorf("namespace import %q has empty __path__", modName)
		}
		for _, p := range origin.Paths {
			if !isTrustedPath(p) {
				return fmt.Errorf("namespace import %q includes untrusted search path %q", modName, p)
			}
		}
		return nil
	default:
		return fmt.Errorf("import %q has unsupported/untrusted origin kind %q (file=%q paths=%v)", modName, origin.Kind, origin.File, origin.Paths)
	}
}

type py14StagedCatalogPin struct {
	ExpectedImageID            string
	ExpectedManifestSHA256     string
	ExpectedWasmSHA256         string
	ExpectedPackagesTreeSHA256 string
	ExpectedEngineTag          string
	ExpectedEngineLibSHA256    string
	RequiredResources          []string
	RequiredPublicImports      map[string][]string
}

type py14ObservedStagedImage struct {
	ImageDir         string
	Manifest         *pythonruntime.ImageManifest
	ActualManifestSH string
	ActualWasmSHA    string
	ActualEngineLibS string
	EngineIdentity   *pythonruntime.NativeEngineIdentity
	PresentFiles     map[string]bool
	ImportedOrigins  map[string]py14GuestImportOrigin
}

func gradePY14StagedPackageCatalog(pin py14StagedCatalogPin, obs py14ObservedStagedImage) error {
	if obs.Manifest == nil {
		return errors.New("missing observed image manifest")
	}
	if obs.Manifest.ImageID != pin.ExpectedImageID {
		return fmt.Errorf("image_id mismatch against shipping selection: got %q want %q", obs.Manifest.ImageID, pin.ExpectedImageID)
	}
	if pin.ExpectedManifestSHA256 != "" && obs.ActualManifestSH != pin.ExpectedManifestSHA256 {
		return fmt.Errorf("image-manifest.json sha256 mismatch against shipping selection: got %q want %q", obs.ActualManifestSH, pin.ExpectedManifestSHA256)
	}
	if obs.ActualWasmSHA != pin.ExpectedWasmSHA256 {
		return fmt.Errorf("python.wasm sha256 mismatch against shipping selection: got %q want %q", obs.ActualWasmSHA, pin.ExpectedWasmSHA256)
	}
	if pin.ExpectedPackagesTreeSHA256 != "" && obs.Manifest.PackagesTreeSHA256 != pin.ExpectedPackagesTreeSHA256 {
		return fmt.Errorf("packages_tree_sha256 mismatch against shipping selection: got %q want %q", obs.Manifest.PackagesTreeSHA256, pin.ExpectedPackagesTreeSHA256)
	}
	if obs.EngineIdentity == nil || obs.EngineIdentity.EngineTag != pin.ExpectedEngineTag {
		return fmt.Errorf("engine_tag mismatch against shipping selection: got %+v want %q", obs.EngineIdentity, pin.ExpectedEngineTag)
	}
	if pin.ExpectedEngineLibSHA256 != "" {
		if obs.EngineIdentity.LibrarySHA256 != pin.ExpectedEngineLibSHA256 {
			return fmt.Errorf("runtime engine library_sha256 mismatch against shipping selection: got %q want %q", obs.EngineIdentity.LibrarySHA256, pin.ExpectedEngineLibSHA256)
		}
		if obs.ActualEngineLibS != "" && obs.ActualEngineLibS != pin.ExpectedEngineLibSHA256 {
			return fmt.Errorf("on-disk staged engine library sha256 mismatch against shipping selection: got %q want %q", obs.ActualEngineLibS, pin.ExpectedEngineLibSHA256)
		}
	}
	for _, res := range pin.RequiredResources {
		if !obs.PresentFiles[res] {
			return fmt.Errorf("missing required image resource %q", res)
		}
	}
	for dist, imports := range pin.RequiredPublicImports {
		for _, imp := range imports {
			origin, ok := obs.ImportedOrigins[imp]
			if !ok {
				return fmt.Errorf("public distribution %q import %q failed resolution from read-only image", dist, imp)
			}
			if err := validateTrustedGuestImportOrigin(imp, origin); err != nil {
				return fmt.Errorf("public distribution %q: %w", dist, err)
			}
		}
	}
	return nil
}

func sha256FileHex(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	contractcheck.FailErr(t, "read file for sha256 "+p, err)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func probeGuestImportOrigins(t *testing.T, svc *portablepython.Service, wsDir, execID string, mods []string) map[string]py14GuestImportOrigin {
	t.Helper()
	importsJSON, err := json.Marshal(mods)
	contractcheck.FailErr(t, "marshal import list", err)
	resp := runPY10Code(t, svc, wsDir, execID, fmt.Sprintf(`
import importlib, json
mods = json.loads(%q)
out = {}
for m in mods:
    mod = importlib.import_module(m)
    spec = getattr(mod, "__spec__", None)
    spec_origin = getattr(spec, "origin", None) if spec is not None else None
    mod_file = getattr(mod, "__file__", None)
    mod_path = getattr(mod, "__path__", None)
    if spec_origin in ("built-in", "frozen"):
        out[m] = {"kind": spec_origin}
    elif mod_file:
        out[m] = {"kind": "file", "file": str(mod_file)}
    elif mod_path is not None:
        out[m] = {"kind": "namespace", "paths": [str(p) for p in list(mod_path)]}
    else:
        out[m] = {"kind": "unknown"}
print(json.dumps(out))
`, string(importsJSON)))

	var resolved map[string]py14GuestImportOrigin
	contractcheck.FailErr(t, "decode guest import origin map", json.Unmarshal([]byte(strings.TrimSpace(resp.Stdout)), &resolved))
	return resolved
}

func TestPY14_StagedPackageGraderAndNegativeControls(t *testing.T) {
	// 1. Read expected pins from the INDEPENDENT checked-in shipping selection
	//    (lycaon/config/runtime/portable-python/bundled-manifest.yaml), never
	//    from the observed staged image/runtime being judged.
	bundled, err := pythonruntime.LoadBundledEngineManifest()
	contractcheck.FailErr(t, "load independent checked-in bundled-manifest.yaml", err)
	if bundled.Image == nil || bundled.Selection == nil {
		t.Fatalf("bundled-manifest.yaml missing Image or Selection pins: %+v", bundled)
	}
	platformArt, err := bundled.ArtifactForPlatform(runtime.GOOS, runtime.GOARCH)
	contractcheck.FailErr(t, "resolve shipping engine artifact for "+runtime.GOOS+"/"+runtime.GOARCH, err)

	// 2. Observe the live staged service and independently hash staged files on disk.
	svc := newPY10Service(t, t.TempDir())
	info := svc.RuntimeInfo()
	imageDir := svc.ImageDir()
	if imageDir == "" {
		t.Fatal("staged Service.ImageDir() is empty")
	}

	manifestPath := filepath.Join(imageDir, "image-manifest.json")
	actualManifestSHA := sha256FileHex(t, manifestPath)
	manifest, err := pythonruntime.LoadImageManifest(manifestPath)
	contractcheck.FailErr(t, "load staged image-manifest.json", err)

	actualWasmSHA := sha256FileHex(t, filepath.Join(imageDir, filepath.FromSlash(manifest.Interpreter.WasmPath)))
	actualEngineLibSHA := sha256FileHex(t, info.NativeEngine.LibraryPath)

	// Enumerate public distributions, imports, and required image resources.
	pubImports := make(map[string][]string)
	var allImports []string
	var requiredResources []string
	presentFiles := make(map[string]bool)

	for _, dist := range manifest.Distributions {
		if !dist.Public {
			continue
		}
		pubImports[dist.NormalizedName] = append([]string(nil), dist.Imports...)
		allImports = append(allImports, dist.Imports...)
		if dist.DistInfoPath != "" {
			rel := filepath.ToSlash(dist.DistInfoPath)
			if !strings.HasPrefix(rel, filepath.ToSlash(manifest.PackagesRoot)+"/") {
				rel = filepath.ToSlash(filepath.Join(manifest.PackagesRoot, rel))
			}
			requiredResources = append(requiredResources, rel)
			if _, statErr := os.Stat(filepath.Join(imageDir, filepath.FromSlash(rel))); statErr == nil {
				presentFiles[rel] = true
			}
		}
	}
	for _, font := range manifest.BundledFonts {
		if font.ImagePath != "" {
			requiredResources = append(requiredResources, font.ImagePath)
			if _, statErr := os.Stat(filepath.Join(imageDir, filepath.FromSlash(font.ImagePath))); statErr == nil {
				presentFiles[font.ImagePath] = true
			}
		}
	}
	if len(pubImports) == 0 || len(requiredResources) == 0 {
		t.Fatalf("staged manifest missing public distributions or resources: dists=%d res=%d", len(pubImports), len(requiredResources))
	}

	// Resolve every public import + explicit builtin/frozen stdlib modules inside
	// the real read-only guest image and inspect their exact origins.
	ws := t.TempDir()
	probeList := append([]string{"sys", "importlib"}, allImports...)
	origins := probeGuestImportOrigins(t, svc, ws, "py14-staged-imports", probeList)
	contractcheck.FailErr(t, "verify builtin sys origin", validateTrustedGuestImportOrigin("sys", origins["sys"]))
	contractcheck.FailErr(t, "verify stdlib importlib origin", validateTrustedGuestImportOrigin("importlib", origins["importlib"]))

	pin := py14StagedCatalogPin{
		ExpectedImageID:            bundled.Image.ImageID,
		ExpectedManifestSHA256:     bundled.Image.ManifestSHA256,
		ExpectedWasmSHA256:         bundled.Image.CPythonWasmSHA256,
		ExpectedPackagesTreeSHA256: bundled.Image.PackagesTreeSHA256,
		ExpectedEngineTag:          bundled.Selection.Tag,
		ExpectedEngineLibSHA256:    platformArt.SHA256,
		RequiredResources:          requiredResources,
		RequiredPublicImports:      pubImports,
	}
	obs := py14ObservedStagedImage{
		ImageDir:         imageDir,
		Manifest:         manifest,
		ActualManifestSH: actualManifestSHA,
		ActualWasmSHA:    actualWasmSHA,
		ActualEngineLibS: actualEngineLibSHA,
		EngineIdentity:   info.NativeEngine,
		PresentFiles:     presentFiles,
		ImportedOrigins:  origins,
	}

	// Positive control: staged bytes/manifest/runtime match independent shipping selection.
	contractcheck.FailErr(t, "grade staged package catalog on "+runtime.GOOS+"/"+runtime.GOARCH, gradePY14StagedPackageCatalog(pin, obs))

	// Negative control A: substitute another valid image while retaining shipping selection pin -> MUST FAIL.
	subManifest := *manifest
	subManifest.ImageID = bundled.Image.ImageID + "-other-valid-build"
	subObs := obs
	subObs.Manifest = &subManifest
	if err := gradePY14StagedPackageCatalog(pin, subObs); err == nil {
		t.Fatal("staged package grader failed to reject substituted valid image")
	}

	// Negative control B: remove a required resource while retaining expected catalog -> MUST FAIL.
	missingResFiles := make(map[string]bool, len(presentFiles))
	for k, v := range presentFiles {
		missingResFiles[k] = v
	}
	delete(missingResFiles, requiredResources[0])
	missingResObs := obs
	missingResObs.PresentFiles = missingResFiles
	if err := gradePY14StagedPackageCatalog(pin, missingResObs); err == nil {
		t.Fatalf("staged package grader failed to reject missing required resource %q", requiredResources[0])
	}

	// Negative control C (wrong-origin control): plant a shadowing module in /workspace/
	// and verify the guest resolves it from /workspace/ and the origin grader REJECTS it!
	shadowWS := t.TempDir()
	shadowFile := filepath.Join(shadowWS, "yaml.py")
	contractcheck.FailErr(t, "write workspace shadowing yaml.py", os.WriteFile(shadowFile, []byte("SHADOWED = True\n"), 0o644))
	shadowOrigins := probeGuestImportOrigins(t, svc, shadowWS, "py14-wrong-origin-shadow", []string{"yaml"})
	if !strings.HasPrefix(shadowOrigins["yaml"].File, "/workspace/") {
		t.Fatalf("expected shadowing yaml.py to resolve under /workspace/, got %+v", shadowOrigins["yaml"])
	}
	wrongOriginMap := make(map[string]py14GuestImportOrigin, len(origins))
	for k, v := range origins {
		wrongOriginMap[k] = v
	}
	wrongOriginMap["yaml"] = shadowOrigins["yaml"]
	wrongOriginObs := obs
	wrongOriginObs.ImportedOrigins = wrongOriginMap
	if err := gradePY14StagedPackageCatalog(pin, wrongOriginObs); err == nil {
		t.Fatalf("staged package grader failed to reject /workspace/ shadowed import origin %+v", shadowOrigins["yaml"])
	}
}

// ---------------------------------------------------------------------------
// PY14 Behavioral Control 4: Real Paired Benchmark Receipt Grader
// (exercises scripts/portable-python-qual/paired/grade_paired_receipt.py —
// the exact same grading entry point invoked by run-paired.sh — against a
// real sanitized baseline receipt and synthetic mutations).
// ---------------------------------------------------------------------------

func runRealPairedReceiptGrader(t *testing.T, kind, receiptPath string, expectedSamples int) (string, error) {
	t.Helper()
	graderScript := filepath.Join("..", "..", "..", "..", "scripts", "portable-python-qual", "paired", "grade_paired_receipt.py")
	if _, err := os.Stat(graderScript); err != nil {
		t.Fatalf("real paired receipt grader script not found at %s: %v", graderScript, err)
	}
	args := []string{graderScript, "--kind", kind, "--receipt", receiptPath}
	if expectedSamples > 0 {
		args = append(args, "--expected-samples", fmt.Sprintf("%d", expectedSamples))
	}
	cmd := exec.Command("python3", args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func loadSanitizedBaselineReceipt(t *testing.T) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "py14", "paired-baseline-receipt-sanitized.json"))
	contractcheck.FailErr(t, "read sanitized real paired baseline receipt", err)
	var data map[string]any
	contractcheck.FailErr(t, "decode sanitized real paired baseline receipt", json.Unmarshal(raw, &data))
	return data
}

func writeJSONTemp(t *testing.T, name string, data map[string]any) string {
	t.Helper()
	raw, err := json.MarshalIndent(data, "", "  ")
	contractcheck.FailErr(t, "marshal "+name, err)
	p := filepath.Join(t.TempDir(), name)
	contractcheck.FailErr(t, "write "+name, os.WriteFile(p, raw, 0o644))
	return p
}

func buildRealCompareReportFromBaseline(base map[string]any) map[string]any {
	eng := base["native_engine"].(map[string]any)
	w0 := base["workloads"].([]any)[0].(map[string]any)
	warmPriming := w0["warm"].(map[string]any)["priming_sample_excluded"]
	return map[string]any{
		"schema_version":        "paintedwolf.portable-python.paired-compare/v2",
		"cold_order":            "alternating-ABAB-fresh-service",
		"warm_protocol":         "reused-service-1-excluded-priming-plus-5-fresh-guests-alternating-workload-order",
		"samples":               5,
		"contaminated":          false,
		"contamination_reasons": []any{},
		"identities": map[string]any{
			"harness_revision":             base["harness_revision"],
			"allow_same_image":             false,
			"same_root":                    false,
			"same_image_and_engine_digest": false,
			"baseline": map[string]any{
				"label":                 "baseline",
				"product_revision":      base["product_revision"],
				"image_id":              base["image_id"],
				"image_manifest_sha256": base["manifest_sha256"],
				"library_sha256":        eng["library_sha256"],
			},
			"candidate": map[string]any{
				"label":                 "candidate",
				"product_revision":      "candidate-rev-py11",
				"image_id":              "pp-cpython-3.12.0-wasi-candidate",
				"image_manifest_sha256": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				"library_sha256":        eng["library_sha256"],
			},
		},
		"priming_samples_excluded": map[string]any{
			"baseline":  map[string]any{"pass_noop": warmPriming},
			"candidate": map[string]any{"pass_noop": warmPriming},
		},
		"comparisons": []any{
			map[string]any{
				"workload":                      "pass_noop",
				"mode":                          "warm",
				"sample_count":                  5,
				"baseline_median_construct_ms":  0.0,
				"candidate_median_construct_ms": 0.0,
				"baseline_median_tool_ms":       164.9,
				"candidate_median_tool_ms":      166.1,
			},
		},
		"regression_flags": []any{},
	}
}

func TestPY14_PairedBenchmarkReceiptGrader(t *testing.T) {
	t.Parallel()

	t.Run("real_sanitized_baseline_receipt_passes", func(t *testing.T) {
		t.Parallel()
		p := writeJSONTemp(t, "baseline-good.json", loadSanitizedBaselineReceipt(t))
		out, err := runRealPairedReceiptGrader(t, "baseline", p, 5)
		if err != nil {
			t.Fatalf("real grader rejected clean sanitized baseline receipt: %v\n%s", err, out)
		}
	})

	t.Run("real_compare_report_passes", func(t *testing.T) {
		t.Parallel()
		cmp := buildRealCompareReportFromBaseline(loadSanitizedBaselineReceipt(t))
		p := writeJSONTemp(t, "compare-good.json", cmp)
		out, err := runRealPairedReceiptGrader(t, "compare", p, 5)
		if err != nil {
			t.Fatalf("real grader rejected clean compare report: %v\n%s", err, out)
		}
	})

	t.Run("rejects_same_actual_image_on_both_sides", func(t *testing.T) {
		t.Parallel()
		base := loadSanitizedBaselineReceipt(t)
		cmp := buildRealCompareReportFromBaseline(base)
		idents := cmp["identities"].(map[string]any)
		cand := idents["candidate"].(map[string]any)
		cand["image_id"] = base["image_id"]
		cand["image_manifest_sha256"] = base["manifest_sha256"]
		p := writeJSONTemp(t, "compare-same-image.json", cmp)
		out, err := runRealPairedReceiptGrader(t, "compare", p, 5)
		if err == nil {
			t.Fatalf("real grader failed to block identical image on both sides:\n%s", out)
		}
		if !strings.Contains(out, "same actual image") {
			t.Fatalf("unexpected grader failure reason:\n%s", out)
		}
	})

	t.Run("rejects_fresh_services_mislabeled_warm", func(t *testing.T) {
		t.Parallel()
		base := loadSanitizedBaselineReceipt(t)
		w0 := base["workloads"].([]any)[0].(map[string]any)
		warmSamples := w0["warm"].(map[string]any)["samples"].([]any)
		warmSamples[1].(map[string]any)["service_construction_ms"] = 1850.5
		p := writeJSONTemp(t, "baseline-fresh-as-warm.json", base)
		out, err := runRealPairedReceiptGrader(t, "baseline", p, 5)
		if err == nil {
			t.Fatalf("real grader failed to block fresh service mislabeled warm:\n%s", out)
		}
		if !strings.Contains(out, "fresh service mislabeled warm") {
			t.Fatalf("unexpected grader failure reason:\n%s", out)
		}
	})

	t.Run("rejects_counted_priming_calls", func(t *testing.T) {
		t.Parallel()
		base := loadSanitizedBaselineReceipt(t)
		w0 := base["workloads"].([]any)[0].(map[string]any)
		warmSamples := w0["warm"].(map[string]any)["samples"].([]any)
		warmSamples[0].(map[string]any)["mode"] = "warm-priming"
		warmSamples[0].(map[string]any)["index"] = 0
		p := writeJSONTemp(t, "baseline-counted-priming.json", base)
		out, err := runRealPairedReceiptGrader(t, "baseline", p, 5)
		if err == nil {
			t.Fatalf("real grader failed to block counted priming call:\n%s", out)
		}
		if !strings.Contains(out, "counted priming call") {
			t.Fatalf("unexpected grader failure reason:\n%s", out)
		}
	})

	t.Run("rejects_contaminated_or_incomplete_samples", func(t *testing.T) {
		t.Parallel()
		// Contaminated flag
		base1 := loadSanitizedBaselineReceipt(t)
		base1["contaminated"] = true
		base1["contamination_reasons"] = []any{"competing build detected"}
		p1 := writeJSONTemp(t, "baseline-contaminated.json", base1)
		if out, err := runRealPairedReceiptGrader(t, "baseline", p1, 5); err == nil {
			t.Fatalf("real grader failed to block contaminated receipt:\n%s", out)
		}

		// Incomplete warm samples
		base2 := loadSanitizedBaselineReceipt(t)
		w0 := base2["workloads"].([]any)[0].(map[string]any)
		warm := w0["warm"].(map[string]any)
		warm["samples"] = warm["samples"].([]any)[:3]
		p2 := writeJSONTemp(t, "baseline-incomplete.json", base2)
		if out, err := runRealPairedReceiptGrader(t, "baseline", p2, 5); err == nil {
			t.Fatalf("real grader failed to block incomplete warm samples:\n%s", out)
		}
	})
}
