package pythonruntime

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

func loadRealCPythonRuntime(t *testing.T, useBootstrap bool) (*WazeroRuntime, *ImageManifest, string) {
	t.Helper()
	imageDir := strings.TrimSpace(os.Getenv("PW_PORTABLE_PYTHON_IMAGE_DIR"))
	if imageDir == "" {
		t.Skip("PW_PORTABLE_PYTHON_IMAGE_DIR not set; skipping real CPython/WASI integration test")
	}

	manifestPath := filepath.Join(imageDir, "image-manifest.json")
	manifest, err := LoadImageManifest(manifestPath)
	testutil.FailErr(t, "load real image manifest", err)
	testutil.FailErr(t, "verify real image", VerifyImage(imageDir, manifest))

	wasmPath, _, _, err := ResolveImagePaths(imageDir, manifest)
	testutil.FailErr(t, "resolve image paths", err)
	wasmBytes, err := os.ReadFile(wasmPath)
	testutil.FailErr(t, "read python.wasm", err)

	ctx := context.Background()
	rt, err := NewWazeroRuntime(ctx, RuntimeConfig{
		WasmBytes:           wasmBytes,
		ImageDir:            imageDir,
		Manifest:            manifest,
		EnforceImportAudit:  true,
		UseTrustedBootstrap: useBootstrap,
		ScratchRoot:         t.TempDir(),
		Clock:               NewSystemClock(),
		Entropy:             CryptoEntropy{},
	})
	testutil.FailErr(t, "create wazero runtime for real CPython/WASI", err)
	t.Cleanup(func() { _ = rt.Close(context.Background()) })
	return rt, manifest, imageDir
}

func TestCPythonWASIManifestAndImportAudit(t *testing.T) {
	rt, manifest, _ := loadRealCPythonRuntime(t, true)
	audit := rt.ImportAudit()
	if audit == nil || !audit.Passed {
		t.Fatalf("expected real python.wasm import audit to pass, got %+v", audit)
	}
	if len(audit.Observed) != 43 {
		t.Fatalf("observed %d WASI imports, want 43", len(audit.Observed))
	}
	if len(manifest.Distributions) < 13 {
		t.Fatalf("expected >= 13 distributions, got %d", len(manifest.Distributions))
	}
}

func TestCPythonWASITrustedBootstrapCompletionAndAbruptExit(t *testing.T) {
	rt, _, _ := loadRealCPythonRuntime(t, true)
	ctx := context.Background()

	t.Run("normal completion", func(t *testing.T) {
		var stdout bytes.Buffer
		ws := newMemWorkspaceAdapter(nil)
		res, err := rt.Execute(ctx, Invocation{
			ExecutionID: "cpython-normal",
			Mode:        ModeCode,
			Code:        "import sys; print('hello-wasi', sys.version_info[:2])",
			Stdout:      &stdout,
			Workspace:   ws,
			Limits:      ResourceLimits{Timeout: 30 * time.Second},
		})
		testutil.FailErr(t, "execute normal CPython", err)
		if !res.CleanCompletion || res.ExitCode != 0 {
			t.Fatalf("expected clean exit 0, got exit=%d clean=%v stderr=%q diag=%v", res.ExitCode, res.CleanCompletion, res.GuestStderr, res.Diagnostics)
		}
		if !strings.Contains(stdout.String(), "hello-wasi (3, 12)") {
			t.Fatalf("unexpected stdout: %q", stdout.String())
		}
	})

	t.Run("os._exit(0) cannot forge CleanCompletion", func(t *testing.T) {
		ws := newMemWorkspaceAdapter(nil)
		res, err := rt.Execute(ctx, Invocation{
			ExecutionID: "cpython-abrupt-exit",
			Mode:        ModeCode,
			Code:        "import os; os._exit(0)",
			Workspace:   ws,
			Limits:      ResourceLimits{Timeout: 30 * time.Second},
		})
		testutil.FailErr(t, "execute os._exit(0)", err)
		if res.ExitCode != 0 {
			t.Fatalf("ExitCode = %d, want 0", res.ExitCode)
		}
		if res.CleanCompletion {
			t.Fatal("os._exit(0) must NOT produce CleanCompletion=true")
		}
	})
}

func TestCPythonWASIMediatedWorkspaceImportsAndStopSealing(t *testing.T) {
	rt, _, _ := loadRealCPythonRuntime(t, true)
	ctx := context.Background()

	t.Run("cwd import crosses WorkspaceSysFS and OperationObserver", func(t *testing.T) {
		ws := newMemWorkspaceAdapter(map[string][]byte{
			"helper.py": []byte("VALUE = 41\n"),
			"data.txt":  []byte("workspace-bytes"),
		})
		var observedPaths []string
		obs := &recordingObserver{
			admitFn: func(_ context.Context, op ReachedOperation) (OperationDecision, error) {
				observedPaths = append(observedPaths, op.Target.RelPath)
				return OperationDecision{Kind: DecisionAllow}, nil
			},
		}
		var stdout bytes.Buffer
		res, err := rt.Execute(ctx, Invocation{
			ExecutionID: "cpython-cwd-import",
			Mode:        ModeCode,
			Code:        "import helper\nprint(helper.VALUE + 1, open('data.txt').read())",
			Stdout:      &stdout,
			Workspace:   ws,
			Observer:    obs,
			Limits:      ResourceLimits{Timeout: 30 * time.Second},
		})
		testutil.FailErr(t, "execute cwd import", err)
		if !res.CleanCompletion || res.ExitCode != 0 {
			t.Fatalf("expected clean exit 0, got exit=%d clean=%v stderr=%q", res.ExitCode, res.CleanCompletion, res.GuestStderr)
		}
		if strings.TrimSpace(stdout.String()) != "42 workspace-bytes" {
			t.Fatalf("stdout = %q, want %q", stdout.String(), "42 workspace-bytes\n")
		}
		seenHelper, seenData := false, false
		for _, p := range observedPaths {
			if p == "helper.py" {
				seenHelper = true
			}
			if p == "data.txt" {
				seenData = true
			}
		}
		if !seenHelper || !seenData {
			t.Fatalf("expected observer to see helper.py and data.txt, saw %v", observedPaths)
		}
	})

	t.Run("script mode sibling import preserves cwd", func(t *testing.T) {
		ws := newMemWorkspaceAdapter(map[string][]byte{
			"sub/main.py":    []byte("import os, sibling\nprint(sibling.MSG, os.getcwd())\n"),
			"sub/sibling.py": []byte("MSG = 'from-sibling'\n"),
		})
		var stdout bytes.Buffer
		res, err := rt.Execute(ctx, Invocation{
			ExecutionID: "cpython-script-import",
			Mode:        ModeScript,
			Script:      "sub/main.py",
			Stdout:      &stdout,
			Workspace:   ws,
			Limits:      ResourceLimits{Timeout: 30 * time.Second},
		})
		testutil.FailErr(t, "execute script mode", err)
		if !res.CleanCompletion || res.ExitCode != 0 {
			t.Fatalf("expected clean exit 0, got exit=%d clean=%v stderr=%q", res.ExitCode, res.CleanCompletion, res.GuestStderr)
		}
		if strings.TrimSpace(stdout.String()) != "from-sibling /workspace" {
			t.Fatalf("stdout = %q, want %q", stdout.String(), "from-sibling /workspace\n")
		}
	})

	t.Run("DecisionStop inside try/finally seals all post-stop output", func(t *testing.T) {
		ws := newMemWorkspaceAdapter(map[string][]byte{
			"secret.env": []byte("TOP_SECRET"),
		})
		obs := &recordingObserver{
			admitFn: func(_ context.Context, op ReachedOperation) (OperationDecision, error) {
				if op.Target.RelPath == "secret.env" {
					return OperationDecision{
						Kind: DecisionStop,
						Stop: &TerminalStop{
							Code:        StopEffectNotAllowed,
							Reason:      "blocked secret read",
							OperationID: op.OperationID,
						},
					}, nil
				}
				return OperationDecision{Kind: DecisionAllow}, nil
			},
		}
		var stdout, stderr bytes.Buffer
		res, err := rt.Execute(ctx, Invocation{
			ExecutionID: "cpython-stop-seal",
			Mode:        ModeCode,
			Code: `
import sys
print("BEFORE_STOP", flush=True)
try:
    open("secret.env").read()
finally:
    print("LEAK_STDOUT", flush=True)
    print("LEAK_STDERR", file=sys.stderr, flush=True)
`,
			Stdout:    &stdout,
			Stderr:    &stderr,
			Workspace: ws,
			Observer:  obs,
			Limits:    ResourceLimits{Timeout: 30 * time.Second},
		})
		testutil.FailErr(t, "execute stop seal", err)
		if res.CleanCompletion {
			t.Fatal("expected CleanCompletion=false after DecisionStop")
		}
		if res.Stop == nil || res.Stop.Code != StopEffectNotAllowed {
			t.Fatalf("expected StopEffectNotAllowed, got %+v", res.Stop)
		}
		if !strings.Contains(stdout.String(), "BEFORE_STOP") {
			t.Fatalf("expected BEFORE_STOP in stdout, got %q", stdout.String())
		}
		if strings.Contains(stdout.String(), "LEAK_STDOUT") || strings.Contains(stderr.String(), "LEAK_STDERR") {
			t.Fatalf("post-stop output leaked! stdout=%q stderr=%q", stdout.String(), stderr.String())
		}
	})
}

func TestCPythonWASILoopCancellationAndBundledPackages(t *testing.T) {
	rt, _, _ := loadRealCPythonRuntime(t, true)
	ctx := context.Background()

	t.Run("pure python infinite loop cancelled by timeout", func(t *testing.T) {
		ws := newMemWorkspaceAdapter(nil)
		res, err := rt.Execute(ctx, Invocation{
			ExecutionID: "cpython-loop-cancel",
			Mode:        ModeCode,
			Code:        "while True:\n    pass\n",
			Workspace:   ws,
			Limits:      ResourceLimits{Timeout: 1500 * time.Millisecond},
		})
		testutil.FailErr(t, "execute infinite loop", err)
		if res.CleanCompletion {
			t.Fatal("expected CleanCompletion=false on cancelled loop")
		}
		if res.Stop == nil || res.Stop.Code != StopCancelled {
			t.Fatalf("expected StopCancelled, got %+v", res.Stop)
		}
	})

	t.Run("bundled packages execute through WazeroRuntime", func(t *testing.T) {
		var stdout bytes.Buffer
		ws := newMemWorkspaceAdapter(nil)
		res, err := rt.Execute(ctx, Invocation{
			ExecutionID: "cpython-bundled-pkgs",
			Mode:        ModeCode,
			Code: `
import io, tomlkit, yaml, openpyxl, pypdf
from jsonschema import Draft202012Validator
from referencing import Registry, Resource
from referencing.jsonschema import DRAFT202012

doc = tomlkit.parse("[pkg]\nname = 'pw' # keep\n")
assert "# keep" in tomlkit.dumps(doc)
assert yaml.safe_load("a: 1\n")["a"] == 1

child = Resource.from_contents({"type": "integer", "minimum": 10}, default_specification=DRAFT202012)
reg = Registry().with_resource("urn:pw:num", child)
v = Draft202012Validator({"$ref": "urn:pw:num"}, registry=reg)
assert v.is_valid(12) and not v.is_valid(3)

wb = openpyxl.Workbook()
wb.active["A1"] = "=SUM(1,2)"
buf = io.BytesIO()
wb.save(buf)
buf.seek(0)
assert openpyxl.load_workbook(buf).active["A1"].value == "=SUM(1,2)"

writer = pypdf.PdfWriter()
writer.add_blank_page(width=72, height=72)
pbuf = io.BytesIO()
writer.write(pbuf)
pbuf.seek(0)
assert len(pypdf.PdfReader(pbuf).pages) == 1
print("ALL_BUNDLED_OK")
`,
			Stdout:    &stdout,
			Workspace: ws,
			Limits:    ResourceLimits{Timeout: 120 * time.Second},
		})
		testutil.FailErr(t, "execute bundled packages", err)
		if !res.CleanCompletion || res.ExitCode != 0 {
			t.Fatalf("expected clean exit 0, got exit=%d clean=%v stderr=%q", res.ExitCode, res.CleanCompletion, res.GuestStderr)
		}
		if strings.TrimSpace(stdout.String()) != "ALL_BUNDLED_OK" {
			t.Fatalf("stdout = %q, want ALL_BUNDLED_OK", stdout.String())
		}
	})
}
