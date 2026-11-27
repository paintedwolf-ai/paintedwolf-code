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
	policy := pythonruntime.DefaultWASIImportPolicy()

	// Construct the exact 43 allowed/mediated wasi_snapshot_preview1 import surface.
	var observed []pythonruntime.WasmImportSpec
	for _, spec := range policy {
		if spec.Disposition == pythonruntime.ImportAllowed || spec.Disposition == pythonruntime.ImportMediated {
			observed = append(observed, pythonruntime.WasmImportSpec{
				Module: spec.Module,
				Name:   spec.Name,
				Kind:   "func",
			})
		}
	}
	if len(observed) != 43 {
		t.Fatalf("baseline observed WASI import count = %d, want 43", len(observed))
	}

	// Positive control: exact 43 declared symbols pass.
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
// (substituting another valid image or removing a required resource while
// retaining the expected catalog).
// ---------------------------------------------------------------------------

type py14StagedCatalogPin struct {
	ExpectedImageID            string
	ExpectedWasmSHA256         string
	ExpectedPackagesTreeSHA256 string
	ExpectedEngineTag          string
	ExpectedEngineLibSHA256    string
	RequiredResources          []string // image-relative resource paths (fonts, dist-info, etc.)
	RequiredPublicImports      map[string][]string
}

type py14ObservedStagedImage struct {
	ImageDir        string
	Manifest        *pythonruntime.ImageManifest
	ActualWasmSHA   string
	EngineIdentity  *pythonruntime.NativeEngineIdentity
	PresentFiles    map[string]bool
	ImportedModules map[string]bool
}

func gradePY14StagedPackageCatalog(pin py14StagedCatalogPin, obs py14ObservedStagedImage) error {
	if obs.Manifest == nil {
		return errors.New("missing observed image manifest")
	}
	if obs.Manifest.ImageID != pin.ExpectedImageID {
		return fmt.Errorf("image_id mismatch: got %q want %q", obs.Manifest.ImageID, pin.ExpectedImageID)
	}
	if obs.ActualWasmSHA != pin.ExpectedWasmSHA256 {
		return fmt.Errorf("wasm_sha256 mismatch: got %q want %q", obs.ActualWasmSHA, pin.ExpectedWasmSHA256)
	}
	if pin.ExpectedPackagesTreeSHA256 != "" && obs.Manifest.PackagesTreeSHA256 != pin.ExpectedPackagesTreeSHA256 {
		return fmt.Errorf("packages_tree_sha256 mismatch: got %q want %q", obs.Manifest.PackagesTreeSHA256, pin.ExpectedPackagesTreeSHA256)
	}
	if obs.EngineIdentity == nil || obs.EngineIdentity.EngineTag != pin.ExpectedEngineTag {
		return fmt.Errorf("engine_tag mismatch: got %+v want %q", obs.EngineIdentity, pin.ExpectedEngineTag)
	}
	if pin.ExpectedEngineLibSHA256 != "" && obs.EngineIdentity.LibrarySHA256 != pin.ExpectedEngineLibSHA256 {
		return fmt.Errorf("engine library_sha256 mismatch: got %q want %q", obs.EngineIdentity.LibrarySHA256, pin.ExpectedEngineLibSHA256)
	}
	for _, res := range pin.RequiredResources {
		if !obs.PresentFiles[res] {
			return fmt.Errorf("missing required image resource %q", res)
		}
	}
	for dist, imports := range pin.RequiredPublicImports {
		for _, imp := range imports {
			if !obs.ImportedModules[imp] {
				return fmt.Errorf("public distribution %q import %q failed resolution from read-only image", dist, imp)
			}
		}
	}
	return nil
}

func TestPY14_StagedPackageGraderAndNegativeControls(t *testing.T) {
	svc := newPY10Service(t, t.TempDir())
	info := svc.RuntimeInfo()
	imageDir := svc.ImageDir()
	if imageDir == "" {
		t.Fatal("staged Service.ImageDir() is empty")
	}

	manifestPath := filepath.Join(imageDir, "image-manifest.json")
	manifest, err := pythonruntime.LoadImageManifest(manifestPath)
	contractcheck.FailErr(t, "load staged image-manifest.json", err)

	// Independently hash the staged python.wasm binary on disk.
	wasmBytes, err := os.ReadFile(filepath.Join(imageDir, filepath.FromSlash(manifest.Interpreter.WasmPath)))
	contractcheck.FailErr(t, "read staged wasm binary", err)
	wasmSum := sha256.Sum256(wasmBytes)
	actualWasmSHA := hex.EncodeToString(wasmSum[:])

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
			rel := filepath.ToSlash(filepath.Join(manifest.PackagesRoot, dist.DistInfoPath))
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

	// Resolve every public import inside the real read-only guest image.
	importsJSON, err := json.Marshal(allImports)
	contractcheck.FailErr(t, "marshal public import list", err)
	ws := t.TempDir()
	resp := runPY10Code(t, svc, ws, "py14-staged-imports", fmt.Sprintf(`
import importlib, json
mods = json.loads(%q)
resolved = {}
for m in mods:
    mod = importlib.import_module(m)
    resolved[m] = getattr(mod, "__file__", "builtin") or "builtin"
print(json.dumps(resolved))
`, string(importsJSON)))

	var resolvedMap map[string]string
	contractcheck.FailErr(t, "decode guest import resolution map", json.Unmarshal([]byte(strings.TrimSpace(resp.Stdout)), &resolvedMap))
	importedOK := make(map[string]bool, len(resolvedMap))
	for m, loc := range resolvedMap {
		if loc == "" {
			t.Fatalf("import %q resolved to empty location", m)
		}
		importedOK[m] = true
	}

	pin := py14StagedCatalogPin{
		ExpectedImageID:            manifest.ImageID,
		ExpectedWasmSHA256:         manifest.Interpreter.WasmSHA256,
		ExpectedPackagesTreeSHA256: manifest.PackagesTreeSHA256,
		ExpectedEngineTag:          info.NativeEngine.EngineTag,
		ExpectedEngineLibSHA256:    info.NativeEngine.LibrarySHA256,
		RequiredResources:          requiredResources,
		RequiredPublicImports:      pubImports,
	}
	obs := py14ObservedStagedImage{
		ImageDir:        imageDir,
		Manifest:        manifest,
		ActualWasmSHA:   actualWasmSHA,
		EngineIdentity:  info.NativeEngine,
		PresentFiles:    presentFiles,
		ImportedModules: importedOK,
	}

	// Positive control: exact staged image + engine + public imports pass.
	contractcheck.FailErr(t, "grade staged package catalog on "+runtime.GOOS+"/"+runtime.GOARCH, gradePY14StagedPackageCatalog(pin, obs))

	// Negative control A: substitute another valid image while retaining expected catalog -> MUST FAIL.
	subManifest := *manifest
	subManifest.ImageID = manifest.ImageID + "-other-valid-build"
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
}

// ---------------------------------------------------------------------------
// PY14 Behavioral Control 4: Paired benchmark receipt grader (pure unit /
// contract controls with synthetic mutations + 1 known-good control).
// ---------------------------------------------------------------------------

type py14BenchmarkSample struct {
	SampleIndex int     `json:"sample_index"`
	ServiceSeq  int     `json:"service_seq"`
	InstanceSeq int     `json:"instance_seq"`
	IsPriming   bool    `json:"is_priming"`
	LatencyMs   float64 `json:"latency_ms"`
	Clean       bool    `json:"clean"`
	ExitCode    int     `json:"exit_code"`
}

type py14BenchmarkSide struct {
	Label               string                `json:"label"`
	Lifecycle           string                `json:"lifecycle"` // "cold" or "warm"
	ExpectedImageID     string                `json:"expected_image_id"`
	ObservedImageID     string                `json:"observed_image_id"`
	ExpectedEngineTag   string                `json:"expected_engine_tag"`
	ObservedEngineTag   string                `json:"observed_engine_tag"`
	PrimingRuns         int                   `json:"priming_runs"`
	ExpectedSamples     int                   `json:"expected_samples"`
	CountedPrimingCalls int                   `json:"counted_priming_calls"`
	Samples             []py14BenchmarkSample `json:"samples"`
}

type py14PairedBenchmarkReceipt struct {
	ComparisonMode       string            `json:"comparison_mode"` // "image_change" or "same_image"
	OverridesUnset       bool              `json:"overrides_unset"`
	HostConditionVerdict string            `json:"host_condition_verdict"` // "clean" or "contaminated"
	Baseline             py14BenchmarkSide `json:"baseline"`
	Candidate            py14BenchmarkSide `json:"candidate"`
}

func gradePY14PairedBenchmarkReceipt(rec py14PairedBenchmarkReceipt) error {
	if !rec.OverridesUnset {
		return errors.New("benchmark contaminated: image/engine env overrides were set")
	}
	if rec.HostConditionVerdict != "clean" {
		return fmt.Errorf("benchmark host condition not clean: %q", rec.HostConditionVerdict)
	}
	if rec.ComparisonMode == "image_change" && rec.Baseline.ObservedImageID == rec.Candidate.ObservedImageID {
		return fmt.Errorf("intended image_change comparison measured identical observed image_id %q on both sides", rec.Baseline.ObservedImageID)
	}
	for _, side := range []py14BenchmarkSide{rec.Baseline, rec.Candidate} {
		if side.ObservedImageID == "" || side.ObservedImageID != side.ExpectedImageID {
			return fmt.Errorf("side %q image_id mismatch: observed=%q expected=%q", side.Label, side.ObservedImageID, side.ExpectedImageID)
		}
		if side.ObservedEngineTag == "" || side.ObservedEngineTag != side.ExpectedEngineTag {
			return fmt.Errorf("side %q engine_tag mismatch: observed=%q expected=%q", side.Label, side.ObservedEngineTag, side.ExpectedEngineTag)
		}
		if side.CountedPrimingCalls > 0 {
			return fmt.Errorf("side %q counted %d priming calls in measured results", side.Label, side.CountedPrimingCalls)
		}
		if len(side.Samples) != side.ExpectedSamples || side.ExpectedSamples <= 0 {
			return fmt.Errorf("side %q incomplete samples: got %d want %d", side.Label, len(side.Samples), side.ExpectedSamples)
		}
		seenInstances := make(map[int]bool, len(side.Samples))
		firstSvcSeq := side.Samples[0].ServiceSeq
		for _, s := range side.Samples {
			if s.IsPriming {
				return fmt.Errorf("side %q sample %d is marked is_priming=true inside measured samples", side.Label, s.SampleIndex)
			}
			if !s.Clean || s.ExitCode != 0 {
				return fmt.Errorf("side %q sample %d contaminated/failed (clean=%v exit=%d)", side.Label, s.SampleIndex, s.Clean, s.ExitCode)
			}
			if seenInstances[s.InstanceSeq] {
				return fmt.Errorf("side %q reused instance_seq %d", side.Label, s.InstanceSeq)
			}
			seenInstances[s.InstanceSeq] = true
			if side.Lifecycle == "warm" && s.ServiceSeq != firstSvcSeq {
				return fmt.Errorf("side %q labeled warm but sample %d used fresh service_seq %d (initial %d)", side.Label, s.SampleIndex, s.ServiceSeq, firstSvcSeq)
			}
		}
	}
	return nil
}

func TestPY14_PairedBenchmarkReceiptGrader(t *testing.T) {
	t.Parallel()

	goodReceipt := func() py14PairedBenchmarkReceipt {
		return py14PairedBenchmarkReceipt{
			ComparisonMode:       "image_change",
			OverridesUnset:       true,
			HostConditionVerdict: "clean",
			Baseline: py14BenchmarkSide{
				Label:               "baseline-py08",
				Lifecycle:           "warm",
				ExpectedImageID:     "pp-cpython-3.12.0-wasi-base",
				ObservedImageID:     "pp-cpython-3.12.0-wasi-base",
				ExpectedEngineTag:   "v0.1.0",
				ObservedEngineTag:   "v0.1.0",
				PrimingRuns:         1,
				ExpectedSamples:     3,
				CountedPrimingCalls: 0,
				Samples: []py14BenchmarkSample{
					{SampleIndex: 1, ServiceSeq: 1, InstanceSeq: 2, LatencyMs: 41.2, Clean: true, ExitCode: 0},
					{SampleIndex: 2, ServiceSeq: 1, InstanceSeq: 3, LatencyMs: 40.8, Clean: true, ExitCode: 0},
					{SampleIndex: 3, ServiceSeq: 1, InstanceSeq: 4, LatencyMs: 41.0, Clean: true, ExitCode: 0},
				},
			},
			Candidate: py14BenchmarkSide{
				Label:               "candidate-py10",
				Lifecycle:           "warm",
				ExpectedImageID:     "pp-cpython-3.12.0-wasi-py10",
				ObservedImageID:     "pp-cpython-3.12.0-wasi-py10",
				ExpectedEngineTag:   "v0.1.0",
				ObservedEngineTag:   "v0.1.0",
				PrimingRuns:         1,
				ExpectedSamples:     3,
				CountedPrimingCalls: 0,
				Samples: []py14BenchmarkSample{
					{SampleIndex: 1, ServiceSeq: 2, InstanceSeq: 6, LatencyMs: 42.1, Clean: true, ExitCode: 0},
					{SampleIndex: 2, ServiceSeq: 2, InstanceSeq: 7, LatencyMs: 41.9, Clean: true, ExitCode: 0},
					{SampleIndex: 3, ServiceSeq: 2, InstanceSeq: 8, LatencyMs: 42.0, Clean: true, ExitCode: 0},
				},
			},
		}
	}

	t.Run("known_good_control_passes", func(t *testing.T) {
		t.Parallel()
		contractcheck.FailErr(t, "known-good paired benchmark receipt", gradePY14PairedBenchmarkReceipt(goodReceipt()))
	})

	t.Run("rejects_same_actual_image_on_both_sides", func(t *testing.T) {
		t.Parallel()
		r := goodReceipt()
		r.Candidate.ExpectedImageID = r.Baseline.ObservedImageID
		r.Candidate.ObservedImageID = r.Baseline.ObservedImageID
		if err := gradePY14PairedBenchmarkReceipt(r); err == nil {
			t.Fatal("grader failed to reject identical actual image on both sides of image_change comparison")
		}
	})

	t.Run("rejects_fresh_services_mislabeled_warm", func(t *testing.T) {
		t.Parallel()
		r := goodReceipt()
		r.Candidate.Samples[1].ServiceSeq = 3 // fresh service on sample 2!
		if err := gradePY14PairedBenchmarkReceipt(r); err == nil {
			t.Fatal("grader failed to reject fresh service mislabeled warm")
		}
	})

	t.Run("rejects_counted_priming_calls", func(t *testing.T) {
		t.Parallel()
		r1 := goodReceipt()
		r1.Baseline.CountedPrimingCalls = 1
		if err := gradePY14PairedBenchmarkReceipt(r1); err == nil {
			t.Fatal("grader failed to reject CountedPrimingCalls > 0")
		}
		r2 := goodReceipt()
		r2.Candidate.Samples[0].IsPriming = true
		if err := gradePY14PairedBenchmarkReceipt(r2); err == nil {
			t.Fatal("grader failed to reject is_priming=true inside measured samples")
		}
	})

	t.Run("rejects_contaminated_or_incomplete_samples", func(t *testing.T) {
		t.Parallel()
		r1 := goodReceipt()
		r1.HostConditionVerdict = "contaminated"
		if err := gradePY14PairedBenchmarkReceipt(r1); err == nil {
			t.Fatal("grader failed to reject contaminated host condition")
		}
		r2 := goodReceipt()
		r2.Candidate.Samples = r2.Candidate.Samples[:2]
		if err := gradePY14PairedBenchmarkReceipt(r2); err == nil {
			t.Fatal("grader failed to reject incomplete sample set")
		}
		r3 := goodReceipt()
		r3.Baseline.Samples[2].Clean = false
		r3.Baseline.Samples[2].ExitCode = 1
		if err := gradePY14PairedBenchmarkReceipt(r3); err == nil {
			t.Fatal("grader failed to reject failed sample")
		}
	})
}
