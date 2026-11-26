package portablepython_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/portablepython"
	"github.com/lycaon/lycaon/internal/pythonadmission"
	"github.com/lycaon/lycaon/internal/pythoncatalog"
	"github.com/lycaon/lycaon/internal/pythondocuments"
	"github.com/lycaon/lycaon/internal/pythonruntime"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/extpackstest"
	"github.com/lycaon/lycaon/internal/toolhost"
	"github.com/lycaon/lycaon/internal/tools"
	pporacle "github.com/lycaon/lycaon/test/contract/portablepython"
)

func newRealPortablePythonService(t *testing.T) *portablepython.Service {
	t.Helper()
	imageDir := strings.TrimSpace(os.Getenv(portablepython.EnvImageDir))
	if imageDir == "" {
		t.Skip("PW_PORTABLE_PYTHON_IMAGE_DIR not set; skipping actual end-to-end CPython/WASI vertical integration test")
	}
	svc, err := portablepython.NewService(context.Background(), portablepython.ServiceConfig{
		ImageDir:    imageDir,
		ScratchRoot: t.TempDir(),
	})
	testutil.FailErr(t, "initialize Portable Python service", err)
	t.Cleanup(func() { _ = svc.Close(context.Background()) })
	return svc
}

type fakeEditorBridge struct {
	mu           sync.Mutex
	drafts       map[string]pythondocuments.EditorDraft
	publishCount int
}

func newFakeEditorBridge() *fakeEditorBridge {
	return &fakeEditorBridge{
		drafts: make(map[string]pythondocuments.EditorDraft),
	}
}

func (e *fakeEditorBridge) SetDraft(relPath, docID, rev string, content []byte, dirty bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.drafts[relPath] = pythondocuments.EditorDraft{
		DocumentID: docID,
		Revision:   rev,
		Bytes:      append([]byte(nil), content...),
		BaseSHA256: pythondocuments.SHA256Hex(content),
		Dirty:      dirty,
	}
}

func (e *fakeEditorBridge) DraftBytes(relPath string) []byte {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]byte(nil), e.drafts[relPath].Bytes...)
}

func (e *fakeEditorBridge) PublishCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.publishCount
}

func (e *fakeEditorBridge) CaptureDraft(_ context.Context, _, _, relPath string) (pythondocuments.EditorDraft, bool, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	d, ok := e.drafts[relPath]
	if !ok {
		return pythondocuments.EditorDraft{}, false, nil
	}
	d.Bytes = append([]byte(nil), d.Bytes...)
	return d, true, nil
}

func (e *fakeEditorBridge) PublishDraft(
	_ context.Context,
	rootID, branch, relPath string,
	baseRev pythondocuments.DocumentRevision,
	data []byte,
) (pythondocuments.DocumentRevision, bool, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	d, ok := e.drafts[relPath]
	if !ok {
		return pythondocuments.DocumentRevision{}, false, nil
	}
	if baseRev.DocumentRevision != "" && baseRev.DocumentRevision != d.Revision {
		return pythondocuments.DocumentRevision{}, true, &pythondocuments.RevisionConflictError{
			Conflict: pythonruntime.RevisionConflict{
				Target:           pythonruntime.CanonicalTarget{Namespace: pythonruntime.NamespaceWorkspace, RootID: rootID, Branch: branch, RelPath: relPath},
				ExpectedRevision: baseRev.Identity(),
				ActualRevision:   d.Revision,
			},
		}
	}
	nextRev := d.Revision + "+1"
	d.Revision = nextRev
	d.Bytes = append([]byte(nil), data...)
	d.BaseSHA256 = pythondocuments.SHA256Hex(data)
	d.Dirty = true
	e.drafts[relPath] = d
	e.publishCount++
	return pythondocuments.DocumentRevision{
		RootID:           rootID,
		Branch:           branch,
		DocumentID:       d.DocumentID,
		DocumentRevision: nextRev,
		DiskSHA256:       d.BaseSHA256,
		UnsavedDraft:     true,
	}, true, nil
}

// TestEndToEndCPythonWASI_DirectToolReadTransformWrite exercises an actual
// end-to-end CPython 3.12.0/WASI run through Service.ExecuteTool with bundled
// tomlkit reading an unsaved editor draft, editing one dependency while
// preserving comments/quotes, and verifying clean-success settlement vs
// uncaught-exception rollback.
func TestEndToEndCPythonWASI_DirectToolReadTransformWrite(t *testing.T) {
	svc := newRealPortablePythonService(t)
	ctx := context.Background()

	wsDir := t.TempDir()
	diskContent := "[project]\nname = 'stale-on-disk'\ndependencies = ['httpx==0.20.0']\n"
	testutil.FailErr(t, "write disk pyproject.toml", os.WriteFile(filepath.Join(wsDir, "pyproject.toml"), []byte(diskContent), 0o644))

	draftContent := strings.Join([]string{
		"# Top-level project comment to preserve",
		"[project]",
		"name = 'demo-service' # keep inline quote style and comment",
		"dependencies = [",
		"    'httpx==0.27.0', # keep pin comment",
		"    'pydantic==2.8.0',",
		"]",
		"",
	}, "\n")

	wantContent := strings.Replace(draftContent, "'httpx==0.27.0'", "'httpx==0.28.1'", 1)

	editor := newFakeEditorBridge()
	editor.SetDraft("pyproject.toml", "doc-pyproject", "editor-rev-7", []byte(draftContent), true)

	roots := []pythondocuments.HostRootConfig{
		{ID: "ws-main", Dir: wsDir},
	}

	t.Run("actual end-to-end CPython/WASI tomlkit edit publishes once with exact bytes", func(t *testing.T) {
		resp, err := svc.ExecuteTool(ctx, portablepython.ToolRequest{
			RawArgs: map[string]any{
				"code": `
import tomlkit

with open("pyproject.toml", "r", encoding="utf-8") as f:
    doc = tomlkit.parse(f.read())

deps = doc["project"]["dependencies"]
for i, item in enumerate(deps):
    if item == "httpx==0.27.0":
        deps[i] = tomlkit.string("httpx==0.28.1", literal=True)

with open("pyproject.toml", "w", encoding="utf-8") as f:
    f.write(tomlkit.dumps(doc))

print("UPDATED_OK")
`,
			},
			CallID:  "call-toml-edit-1",
			Actor:   "coordinator",
			View:    "primary",
			Surface: "agent",
			Roots:   roots,
			Editor:  editor,
			Limits:  pythonruntime.ResourceLimits{Timeout: 60 * time.Second},
		})
		testutil.FailErr(t, "execute direct python tomlkit edit", err)
		if !resp.CleanCompletion || resp.ExitCode != 0 {
			t.Fatalf("expected clean completion exit 0, got exit=%d clean=%v stderr=%q stop=%+v", resp.ExitCode, resp.CleanCompletion, resp.Stderr, resp.Stop)
		}
		if strings.TrimSpace(resp.Stdout) != "UPDATED_OK" {
			t.Fatalf("stdout = %q, want UPDATED_OK", resp.Stdout)
		}
		gotDraft := string(editor.DraftBytes("pyproject.toml"))
		if gotDraft != wantContent {
			t.Fatalf("published draft mismatch:\ngot:\n%s\nwant:\n%s", gotDraft, wantContent)
		}
		if editor.PublishCount() != 1 {
			t.Fatalf("editor PublishCount = %d, want 1", editor.PublishCount())
		}
		if resp.Settlement == nil || len(resp.Settlement.Published) != 1 {
			t.Fatalf("expected exactly 1 settlement publication, got %+v", resp.Settlement)
		}
		pubs := resp.DocumentEvidence.Publications()
		if len(pubs) != 1 {
			t.Fatalf("expected 1 DocumentEvidence publication record, got %d", len(pubs))
		}
		reads := resp.DocumentEvidence.Reads()
		sawUnsavedRead := false
		for _, r := range reads {
			if r.Target.RelPath == "pyproject.toml" && r.UnsavedDraft && r.BytesDeliveredToGuest > 0 {
				sawUnsavedRead = true
			}
		}
		if !sawUnsavedRead {
			t.Fatalf("expected DocumentReadRecord for unsaved draft pyproject.toml with delivered bytes > 0, got %+v", reads)
		}
	})

	t.Run("actual end-to-end CPython/WASI uncaught exception leaves zero host mutations", func(t *testing.T) {
		beforeDraft := editor.DraftBytes("pyproject.toml")
		beforePublishes := editor.PublishCount()

		resp, err := svc.ExecuteTool(ctx, portablepython.ToolRequest{
			RawArgs: map[string]any{
				"code": `
with open("pyproject.toml", "w", encoding="utf-8") as f:
    f.write("CORRUPTED = true\n")
raise RuntimeError("abort before settlement")
`,
			},
			CallID:  "call-toml-fail-2",
			Actor:   "coordinator",
			View:    "primary",
			Surface: "agent",
			Roots:   roots,
			Editor:  editor,
			Limits:  pythonruntime.ResourceLimits{Timeout: 60 * time.Second},
		})
		testutil.FailErr(t, "execute failing python script", err)
		if resp.CleanCompletion {
			t.Fatal("expected CleanCompletion=false on uncaught exception")
		}
		if resp.ExitCode == 0 {
			t.Fatalf("expected non-zero ExitCode on uncaught exception, got %d", resp.ExitCode)
		}
		if editor.PublishCount() != beforePublishes {
			t.Fatalf("expected 0 new host publications on uncaught exception, got %d", editor.PublishCount()-beforePublishes)
		}
		if !bytes.Equal(editor.DraftBytes("pyproject.toml"), beforeDraft) {
			t.Fatalf("draft bytes were mutated despite uncaught exception!")
		}
		if resp.Settlement != nil && len(resp.Settlement.Published) != 0 {
			t.Fatalf("expected 0 settlement publications, got %+v", resp.Settlement.Published)
		}
	})

	t.Run("manifest-backed read-only pip inventory and package help", func(t *testing.T) {
		listResp, err := svc.ExecuteTool(ctx, portablepython.ToolRequest{
			RawArgs: map[string]any{
				"module": "pip",
				"args":   []string{"list", "--format=json"},
			},
		})
		testutil.FailErr(t, "execute pip list --format=json", err)
		if listResp.ExitCode != 0 || !listResp.CleanCompletion {
			t.Fatalf("pip list failed: exit=%d stderr=%q", listResp.ExitCode, listResp.Stderr)
		}
		var rows []struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		}
		testutil.FailErr(t, "parse pip list json", json.Unmarshal([]byte(listResp.Stdout), &rows))
		if len(rows) < 13 {
			t.Fatalf("expected >= 13 installed packages, got %d", len(rows))
		}

		installResp, err := svc.ExecuteTool(ctx, portablepython.ToolRequest{
			RawArgs: map[string]any{
				"module": "pip",
				"args":   []string{"install", "numpy"},
			},
		})
		testutil.FailErr(t, "execute pip install refusal", err)
		if installResp.ExitCode == 0 || installResp.Diagnostic == nil || installResp.Diagnostic.Kind != pythoncatalog.DiagnosticImageMutation {
			t.Fatalf("expected DiagnosticImageMutation refusal for pip install, got %+v", installResp)
		}
	})
}

// TestEndToEndCPythonWASI_GuestRequestsSessionThroughHost exercises REAL guest
// Python (`import requests; ...`) inside CPython/WASI crossing the WASI host
// bridge into pythonadmission.GuestHTTPAdapter -> HTTPTransport ->
// AdmissionObserver.Admit(EffectHTTP).
func TestEndToEndCPythonWASI_GuestRequestsSessionThroughHost(t *testing.T) {
	svc := newRealPortablePythonService(t)
	ctx := context.Background()

	var refusedHits atomic.Int64
	refusedSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		refusedHits.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("SHOULD_NEVER_BE_REACHED"))
	}))
	defer refusedSrv.Close()

	allowedTargetSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("REDIRECT_TARGET_OK"))
	}))
	defer allowedTargetSrv.Close()

	allowedSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"accepted","source":"loopback"}`))
		case "/redir-ok":
			http.Redirect(w, r, allowedTargetSrv.URL+"/final", http.StatusFound)
		case "/redir-blocked":
			http.Redirect(w, r, refusedSrv.URL+"/secret", http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	defer allowedSrv.Close()

	allowedURL, _ := url.Parse(allowedSrv.URL)
	allowedTargetURL, _ := url.Parse(allowedTargetSrv.URL)
	refusedURL, _ := url.Parse(refusedSrv.URL)

	policy := func(_ context.Context, op pythonruntime.ReachedOperation) pythonadmission.PolicyDecision {
		if op.Family == pythonruntime.EffectHTTP {
			host := op.Target.RootID
			if host == allowedURL.Host || host == allowedTargetURL.Host {
				return pythonadmission.PolicyDecision{Kind: pythonruntime.DecisionAllow}
			}
			if host == refusedURL.Host {
				return pythonadmission.PolicyDecision{
					Kind:      pythonruntime.DecisionStop,
					Condition: pythonadmission.ConditionEffectDenied,
					Reason:    "destination host " + host + " is refused by egress policy",
				}
			}
		}
		return pythonadmission.PolicyDecision{Kind: pythonruntime.DecisionAllow}
	}

	wsDir := t.TempDir()
	roots := []pythondocuments.HostRootConfig{{ID: "ws-http", Dir: wsDir}}

	t.Run("actual end-to-end CPython/WASI accepted destination", func(t *testing.T) {
		resp, err := svc.ExecuteTool(ctx, portablepython.ToolRequest{
			RawArgs: map[string]any{
				"code": `
import json, requests, sys
r = requests.get(sys.argv[1])
assert r.status_code == 200, r.status_code
data = r.json()
assert data["status"] == "accepted"
print(json.dumps({"status": r.status_code, "body": data}))
`,
				"args": []string{allowedSrv.URL + "/ok"},
			},
			CallID:            "call-http-ok",
			Actor:             "coordinator",
			View:              "primary",
			Surface:           "agent",
			Roots:             roots,
			Policy:            policy,
			AllowLoopbackHTTP: true,
			Limits:            pythonruntime.ResourceLimits{Timeout: 60 * time.Second},
		})
		testutil.FailErr(t, "execute guest requests accepted destination", err)
		if !resp.CleanCompletion || resp.ExitCode != 0 {
			t.Fatalf("expected clean exit 0, got exit=%d clean=%v stderr=%q stop=%+v", resp.ExitCode, resp.CleanCompletion, resp.Stderr, resp.Stop)
		}
		if !strings.Contains(resp.Stdout, `"accepted"`) {
			t.Fatalf("unexpected stdout: %q", resp.Stdout)
		}
		httpRecs := resp.AdmissionEvidence.HTTPRecords()
		if len(httpRecs) != 1 || httpRecs[0].Status != 200 || httpRecs[0].BytesReceived == 0 {
			t.Fatalf("expected 1 HTTPRecord with status 200 and bytes > 0, got %+v", httpRecs)
		}
	})

	t.Run("actual end-to-end CPython/WASI allowed redirect records hop", func(t *testing.T) {
		resp, err := svc.ExecuteTool(ctx, portablepython.ToolRequest{
			RawArgs: map[string]any{
				"code": `
import requests, sys
s = requests.Session()
r = s.get(sys.argv[1])
assert r.status_code == 200, r.status_code
assert r.text == "REDIRECT_TARGET_OK", r.text
assert r.url.endswith("/final"), r.url
assert len(r.history) == 1, len(r.history)
print("REDIR_OK", r.url, r.text)
`,
				"args": []string{allowedSrv.URL + "/redir-ok"},
			},
			CallID:            "call-http-redir-ok",
			Actor:             "coordinator",
			View:              "primary",
			Surface:           "agent",
			Roots:             roots,
			Policy:            policy,
			AllowLoopbackHTTP: true,
			Limits:            pythonruntime.ResourceLimits{Timeout: 60 * time.Second},
		})
		testutil.FailErr(t, "execute guest requests allowed redirect", err)
		if !resp.CleanCompletion || resp.ExitCode != 0 {
			t.Fatalf("expected clean exit 0, got exit=%d clean=%v stderr=%q stop=%+v", resp.ExitCode, resp.CleanCompletion, resp.Stderr, resp.Stop)
		}
		if !strings.Contains(resp.Stdout, "REDIR_OK") {
			t.Fatalf("unexpected stdout: %q", resp.Stdout)
		}
		httpRecs := resp.AdmissionEvidence.HTTPRecords()
		if len(httpRecs) != 1 || len(httpRecs[0].Redirects) != 1 || !httpRecs[0].Redirects[0].Admitted {
			t.Fatalf("expected 1 HTTPRecord with 1 admitted redirect hop, got %+v", httpRecs)
		}
	})

	t.Run("actual end-to-end CPython/WASI refused redirect blocked before connecting (0 hits)", func(t *testing.T) {
		refusedHits.Store(0)
		resp, err := svc.ExecuteTool(ctx, portablepython.ToolRequest{
			RawArgs: map[string]any{
				"code": `
import requests, sys
requests.get(sys.argv[1])
print("SHOULD_NOT_PRINT")
`,
				"args": []string{allowedSrv.URL + "/redir-blocked"},
			},
			CallID:            "call-http-redir-blocked",
			Actor:             "coordinator",
			View:              "primary",
			Surface:           "agent",
			Roots:             roots,
			Policy:            policy,
			AllowLoopbackHTTP: true,
			Limits:            pythonruntime.ResourceLimits{Timeout: 60 * time.Second},
		})
		testutil.FailErr(t, "execute guest requests refused redirect", err)
		if refusedHits.Load() != 0 {
			t.Fatalf("refused redirect target was hit %d time(s); must be 0!", refusedHits.Load())
		}
		if resp.CleanCompletion {
			t.Fatal("expected CleanCompletion=false when redirect is refused")
		}
		if resp.Stop == nil || resp.Stop.Code != pythonruntime.StopEffectNotAllowed {
			t.Fatalf("expected StopEffectNotAllowed, got %+v (stderr=%q)", resp.Stop, resp.Stderr)
		}
		if strings.Contains(resp.Stdout, "SHOULD_NOT_PRINT") {
			t.Fatalf("post-stop output leaked into stdout: %q", resp.Stdout)
		}
	})

	t.Run("actual end-to-end CPython/WASI refused direct destination blocked before connecting (0 hits)", func(t *testing.T) {
		refusedHits.Store(0)
		resp, err := svc.ExecuteTool(ctx, portablepython.ToolRequest{
			RawArgs: map[string]any{
				"code": `
import requests, sys
requests.get(sys.argv[1])
`,
				"args": []string{refusedSrv.URL + "/direct"},
			},
			CallID:            "call-http-direct-blocked",
			Actor:             "coordinator",
			View:              "primary",
			Surface:           "agent",
			Roots:             roots,
			Policy:            policy,
			AllowLoopbackHTTP: true,
			Limits:            pythonruntime.ResourceLimits{Timeout: 60 * time.Second},
		})
		testutil.FailErr(t, "execute guest requests refused direct destination", err)
		if refusedHits.Load() != 0 {
			t.Fatalf("refused direct server was hit %d time(s); must be 0!", refusedHits.Load())
		}
		if resp.CleanCompletion {
			t.Fatal("expected CleanCompletion=false on refused destination")
		}
		if resp.Stop == nil || resp.Stop.Code != pythonruntime.StopEffectNotAllowed {
			t.Fatalf("expected StopEffectNotAllowed, got %+v", resp.Stop)
		}
	})
}

// TestDirectPythonToolSchemaAndValidation checks the projected `python` tool
// schema description verbatim, rejects invalid code+script combinations, and
// verifies toolhost registration wiring (both unconfigured and configured).
func TestDirectPythonToolSchemaAndValidation(t *testing.T) {
	stockCatalog := extpackstest.StockCatalog(t)
	schemas, err := extpacks.LoadEffectiveRegistrySchemas(stockCatalog)
	testutil.FailErr(t, "load effective registry schemas", err)

	entry, ok := schemas.Tools["python"]
	if !ok {
		t.Fatal("expected python tool in effective registry schemas")
	}
	const wantDesc = "Run helper scripts here with the standard library and fixed packages. Run project code and tests in the project environment."
	if entry.Description != wantDesc {
		t.Fatalf("python tool description mismatch:\ngot:  %q\nwant: %q", entry.Description, wantDesc)
	}
	if entry.Description != pythoncatalog.ToolDescription {
		t.Fatalf("schema description %q != pythoncatalog.ToolDescription %q", entry.Description, pythoncatalog.ToolDescription)
	}

	// Reject invalid code+script combination.
	if _, err := pythoncatalog.ValidateDirectToolArgs(map[string]any{
		"code":   "print(1)",
		"script": "foo.py",
	}); err == nil {
		t.Fatal("expected ValidateDirectToolArgs to reject code+script combination")
	}

	// Unconfigured toolhost.Runtime preserves clean error and still validates args.
	rt, err := toolhost.NewRuntime(toolhost.RuntimeConfig{Catalog: stockCatalog})
	testutil.FailErr(t, "construct toolhost runtime without portable python", err)
	if _, err := rt.Registry.Run(context.Background(), "python", map[string]any{
		"code":   "print(1)",
		"script": "foo.py",
	}, tools.ToolContext{}); err == nil {
		t.Fatal("expected registered python handler to reject code+script combination")
	}
	_, err = rt.Registry.Run(context.Background(), "python", map[string]any{
		"code": "print(1)",
	}, tools.ToolContext{})
	if err == nil || !strings.Contains(err.Error(), "portable python runtime is not configured") {
		t.Fatalf("expected unconfigured error, got %v", err)
	}

	// When PW_PORTABLE_PYTHON_IMAGE_DIR is set, verify live toolhost dispatch!
	if strings.TrimSpace(os.Getenv(portablepython.EnvImageDir)) != "" {
		svc := newRealPortablePythonService(t)
		rt.Commands.SetPortablePython(svc)
		raw, err := rt.Registry.Run(context.Background(), "python", map[string]any{
			"code": "print('TOOLHOST_LIVE_OK')",
		}, tools.ToolContext{})
		testutil.FailErr(t, "execute python tool via toolhost.Registry", err)
		var decoded portablepython.ToolResponse
		testutil.FailErr(t, "decode toolhost python response", json.Unmarshal([]byte(raw), &decoded))
		if !decoded.CleanCompletion || decoded.ExitCode != 0 || strings.TrimSpace(decoded.Stdout) != "TOOLHOST_LIVE_OK" {
			t.Fatalf("unexpected toolhost python response: %+v", decoded)
		}
	}
}

func loadFrozenFixtureZip(t *testing.T) map[string][]byte {
	t.Helper()
	zipPath := filepath.Join("..", "..", "test", "contract", "portablepython", "testdata", "source-fixtures.zip")
	raw, err := os.ReadFile(zipPath)
	testutil.FailErr(t, "read frozen source-fixtures.zip", err)
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	testutil.FailErr(t, "open frozen source-fixtures.zip", err)
	files := make(map[string][]byte, len(zr.File))
	for _, f := range zr.File {
		rc, err := f.Open()
		testutil.FailErr(t, "open zip member "+f.Name, err)
		data, err := io.ReadAll(rc)
		_ = rc.Close()
		testutil.FailErr(t, "read zip member "+f.Name, err)
		files[f.Name] = data
	}
	return files
}

func extractWorkflowDir(t *testing.T, files map[string][]byte, prefix string) string {
	t.Helper()
	dir := t.TempDir()
	for name, data := range files {
		if !strings.HasPrefix(name, prefix+"/") {
			continue
		}
		rel := strings.TrimPrefix(name, prefix+"/")
		dst := filepath.Join(dir, filepath.FromSlash(rel))
		testutil.FailErr(t, "mkdir "+dst, os.MkdirAll(filepath.Dir(dst), 0o755))
		testutil.FailErr(t, "write "+dst, os.WriteFile(dst, data, 0o644))
	}
	return dir
}

// TestEndToEndCPythonWASI_FrozenWorkloadReplay runs the frozen agent programs
// (`dependency-review` with tomllib+csv and `workbook` with openpyxl+PyYAML)
// inside actual Portable Python CPython/WASI through Service.ExecuteTool and
// grades their real settled outputs with PY06's independent oracles.
func TestEndToEndCPythonWASI_FrozenWorkloadReplay(t *testing.T) {
	svc := newRealPortablePythonService(t)
	ctx := context.Background()
	files := loadFrozenFixtureZip(t)

	t.Run("actual end-to-end CPython/WASI frozen dependency-review (tomllib + csv)", func(t *testing.T) {
		depDir := extractWorkflowDir(t, files, "dependency-review")
		resp, err := svc.ExecuteTool(ctx, portablepython.ToolRequest{
			RawArgs: map[string]any{
				"script": "program.py",
			},
			CallID:  "call-frozen-dep-review",
			Actor:   "coordinator",
			View:    "primary",
			Surface: "agent",
			Roots:   []pythondocuments.HostRootConfig{{ID: "ws-dep", Dir: depDir}},
			Limits:  pythonruntime.ResourceLimits{Timeout: 90 * time.Second},
		})
		testutil.FailErr(t, "execute frozen dependency-review in CPython/WASI", err)
		if !resp.CleanCompletion || resp.ExitCode != 0 {
			t.Fatalf("dependency-review failed: exit=%d clean=%v stderr=%q stop=%+v", resp.ExitCode, resp.CleanCompletion, resp.Stderr, resp.Stop)
		}

		depCSV, err := os.ReadFile(filepath.Join(depDir, "dependency-changes.csv"))
		testutil.FailErr(t, "read settled dependency-changes.csv", err)
		depExp, err := pporacle.ComputeDepDiff(
			files["dependency-review/before/uv.lock"],
			files["dependency-review/after/uv.lock"],
			files["dependency-review/before/train-host/requirements-cuda.txt"],
			files["dependency-review/after/train-host/requirements-cuda.txt"],
		)
		testutil.FailErr(t, "compute expected dep diff", err)
		testutil.FailErr(t, "grade dependency-changes.csv with PY06 oracle", pporacle.GradeDependencyDiff(depExp, depCSV, map[string][]string{"before": nil, "after": nil}))
	})

	t.Run("actual end-to-end CPython/WASI frozen workbook (openpyxl + PyYAML)", func(t *testing.T) {
		wbDir := extractWorkflowDir(t, files, "workbook")
		resp, err := svc.ExecuteTool(ctx, portablepython.ToolRequest{
			RawArgs: map[string]any{
				"script": "program.py",
			},
			CallID:  "call-frozen-workbook",
			Actor:   "coordinator",
			View:    "primary",
			Surface: "agent",
			Roots:   []pythondocuments.HostRootConfig{{ID: "ws-wb", Dir: wbDir}},
			Limits:  pythonruntime.ResourceLimits{Timeout: 120 * time.Second},
		})
		testutil.FailErr(t, "execute frozen workbook in CPython/WASI", err)
		if !resp.CleanCompletion || resp.ExitCode != 0 {
			t.Fatalf("workbook failed: exit=%d clean=%v stderr=%q stop=%+v", resp.ExitCode, resp.CleanCompletion, resp.Stderr, resp.Stop)
		}

		wbBytes, err := os.ReadFile(filepath.Join(wbDir, "repositories.xlsx"))
		testutil.FailErr(t, "read settled repositories.xlsx", err)
		testutil.FailErr(t, "grade repositories.xlsx (frozen replay) with PY06 oracle", pporacle.GradeRepoWorkbookWithMode(files["workbook/config/repos.yaml"], wbBytes, pporacle.WorkbookFrozenReplay))
		testutil.FailErr(t, "grade repositories.xlsx (live mode) with PY06 oracle", pporacle.GradeRepoWorkbookWithMode(files["workbook/config/repos.yaml"], wbBytes, pporacle.WorkbookLiveDeliverable))
	})
}
