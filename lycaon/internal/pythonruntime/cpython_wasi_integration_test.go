package pythonruntime

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
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

	t.Run("failed stdout write prevents CleanCompletion", func(t *testing.T) {
		ws := newMemWorkspaceAdapter(nil)
		res, err := rt.Execute(ctx, Invocation{
			ExecutionID: "cpython-failed-stdout",
			Mode:        ModeCode,
			Code:        "print('boom', flush=True)",
			Stdout:      failingWriter{},
			Workspace:   ws,
			Limits:      ResourceLimits{Timeout: 30 * time.Second},
		})
		testutil.FailErr(t, "execute with failing stdout", err)
		if res.CleanCompletion {
			t.Fatal("failed stdout write must NOT produce CleanCompletion=true")
		}
	})

	t.Run("guest cannot forge completion via /.pw_internal/complete", func(t *testing.T) {
		ws := newMemWorkspaceAdapter(nil)
		res, err := rt.Execute(ctx, Invocation{
			ExecutionID: "cpython-forge-attempt",
			Mode:        ModeCode,
			Code: `
import os
try:
    with open("/.pw_internal/complete", "wb") as f:
        f.write(b"forged")
except OSError:
    pass
os._exit(0)
`,
			Workspace: ws,
			Limits:    ResourceLimits{Timeout: 30 * time.Second},
		})
		testutil.FailErr(t, "execute forge attempt", err)
		if res.CleanCompletion {
			t.Fatal("guest writing /.pw_internal/complete + os._exit(0) must NOT produce CleanCompletion=true")
		}
	})
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, errors.New("simulated downstream pipe error")
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

	t.Run("deterministic Wasm fuel stops pure-Python loop without timeout", func(t *testing.T) {
		ws := newMemWorkspaceAdapter(nil)
		res, err := rt.Execute(ctx, Invocation{
			ExecutionID: "cpython-fuel-py-loop",
			Mode:        ModeCode,
			Code:        "while True:\n    pass\n",
			Workspace:   ws,
			Limits: ResourceLimits{
				MaxGuestWork: 2_000_000,
				Timeout:      120 * time.Second,
			},
		})
		testutil.FailErr(t, "execute fuel-bounded python loop", err)
		if res.CleanCompletion {
			t.Fatal("expected CleanCompletion=false on fuel exhaustion")
		}
		if res.Stop == nil || res.Stop.Code != StopWorkLimitExceeded {
			t.Fatalf("expected StopWorkLimitExceeded, got %+v (guestWork=%d)", res.Stop, res.Usage.GuestWork)
		}
	})

	t.Run("deterministic Wasm fuel stops compiled C regex backtracking without timeout", func(t *testing.T) {
		ws := newMemWorkspaceAdapter(nil)
		res, err := rt.Execute(ctx, Invocation{
			ExecutionID: "cpython-fuel-c-regex",
			Mode:        ModeCode,
			Code:        "import re\nre.match(r'(a+)+b', 'a' * 28 + 'c')\n",
			Workspace:   ws,
			Limits: ResourceLimits{
				MaxGuestWork: 3_000_000,
				Timeout:      120 * time.Second,
			},
		})
		testutil.FailErr(t, "execute fuel-bounded C regex", err)
		if res.CleanCompletion {
			t.Fatal("expected CleanCompletion=false on C regex fuel exhaustion")
		}
		if res.Stop == nil || res.Stop.Code != StopWorkLimitExceeded {
			t.Fatalf("expected StopWorkLimitExceeded, got %+v (guestWork=%d)", res.Stop, res.Usage.GuestWork)
		}
	})
}

func TestCPythonWASIImageTamperNegativeControls(t *testing.T) {
	imageDir := strings.TrimSpace(os.Getenv("PW_PORTABLE_PYTHON_IMAGE_DIR"))
	if imageDir == "" {
		t.Skip("PW_PORTABLE_PYTHON_IMAGE_DIR not set; skipping image tamper negative controls")
	}

	copyDir := func(t *testing.T, src, dst string) {
		t.Helper()
		err := filepath.WalkDir(src, func(p string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			rel, err := filepath.Rel(src, p)
			if err != nil {
				return err
			}
			target := filepath.Join(dst, rel)
			if d.IsDir() {
				return os.MkdirAll(target, 0o755)
			}
			in, err := os.Open(p)
			if err != nil {
				return err
			}
			defer in.Close()
			out, err := os.Create(target)
			if err != nil {
				return err
			}
			defer out.Close()
			_, err = io.Copy(out, in)
			return err
		})
		testutil.FailErr(t, "copy image dir", err)
	}

	t.Run("modifying package resource certifi/cacert.pem fails VerifyImage", func(t *testing.T) {
		tmp := t.TempDir()
		copyDir(t, imageDir, tmp)
		manifest, err := LoadImageManifest(filepath.Join(tmp, "image-manifest.json"))
		testutil.FailErr(t, "load manifest", err)
		testutil.FailErr(t, "verify untouched copy", VerifyImage(tmp, manifest))

		cacert := filepath.Join(tmp, "packages", "certifi", "cacert.pem")
		orig, err := os.ReadFile(cacert)
		testutil.FailErr(t, "read cacert.pem", err)
		testutil.FailErr(t, "tamper cacert.pem", os.WriteFile(cacert, append(orig, []byte("\n# tampered\n")...), 0o644))

		if err := VerifyImage(tmp, manifest); err == nil {
			t.Fatal("VerifyImage must reject modified packages/certifi/cacert.pem")
		}
	})

	t.Run("adding unlisted .py file under packages/ fails VerifyImage", func(t *testing.T) {
		tmp := t.TempDir()
		copyDir(t, imageDir, tmp)
		manifest, err := LoadImageManifest(filepath.Join(tmp, "image-manifest.json"))
		testutil.FailErr(t, "load manifest", err)

		unlisted := filepath.Join(tmp, "packages", "unlisted_injected.py")
		testutil.FailErr(t, "write unlisted file", os.WriteFile(unlisted, []byte("INJECTED = True\n"), 0o644))

		if err := VerifyImage(tmp, manifest); err == nil {
			t.Fatal("VerifyImage must reject unlisted .py file under packages/")
		}
	})
}

func TestCPythonWASIPerInvocationScratchIsolation(t *testing.T) {
	rt, _, _ := loadRealCPythonRuntime(t, true)
	ctx := context.Background()

	// Call 1 writes /tmp/leak.txt.
	var out1 bytes.Buffer
	res1, err := rt.Execute(ctx, Invocation{
		ExecutionID: "scratch-call-1",
		Mode:        ModeCode,
		Code:        "open('/tmp/leak.txt', 'w').write('secret'); import os; print(os.path.exists('/tmp/leak.txt'))",
		Stdout:      &out1,
		Workspace:   newMemWorkspaceAdapter(nil),
		Limits:      ResourceLimits{Timeout: 30 * time.Second},
	})
	testutil.FailErr(t, "scratch call 1", err)
	if !res1.CleanCompletion || strings.TrimSpace(out1.String()) != "True" {
		t.Fatalf("call 1 failed: clean=%v out=%q stderr=%q", res1.CleanCompletion, out1.String(), res1.GuestStderr)
	}

	// Call 2 must NOT see /tmp/leak.txt.
	var out2 bytes.Buffer
	res2, err := rt.Execute(ctx, Invocation{
		ExecutionID: "scratch-call-2",
		Mode:        ModeCode,
		Code:        "import os; print(os.path.exists('/tmp/leak.txt'))",
		Stdout:      &out2,
		Workspace:   newMemWorkspaceAdapter(nil),
		Limits:      ResourceLimits{Timeout: 30 * time.Second},
	})
	testutil.FailErr(t, "scratch call 2", err)
	if !res2.CleanCompletion || strings.TrimSpace(out2.String()) != "False" {
		t.Fatalf("call 2 leaked scratch! clean=%v out=%q stderr=%q", res2.CleanCompletion, out2.String(), res2.GuestStderr)
	}
}

func TestCPythonWASISubdirectoryCWD(t *testing.T) {
	rt, _, _ := loadRealCPythonRuntime(t, true)
	ctx := context.Background()

	files := map[string][]byte{
		"root.txt":                   []byte("root-data"),
		"bench/data.txt":             []byte("bench-data"),
		"tools/helper/info.txt":      []byte("helper-data"),
		"tools/helper/run.py":        []byte("from pathlib import Path\nprint(Path.cwd(), open('info.txt').read())\n"),
		"tools/helper/test_math.py":  []byte("import unittest\nclass T(unittest.TestCase):\n    def test_add(self): self.assertEqual(2+2, 4)\n"),
	}

	for _, tc := range []struct {
		name    string
		cwd     string
		mode    ExecutionMode
		code    string
		script  string
		module  string
		args    []string
		wantSub string
	}{
		{
			name:    "empty cwd code",
			cwd:     "",
			mode:    ModeCode,
			code:    "from pathlib import Path; print(Path.cwd(), open('root.txt').read())",
			wantSub: "/workspace root-data",
		},
		{
			name:    "bench cwd code",
			cwd:     "bench",
			mode:    ModeCode,
			code:    "from pathlib import Path; print(Path.cwd(), open('data.txt').read())",
			wantSub: "/workspace/bench bench-data",
		},
		{
			name:    "dot-slash tools/helper cwd code",
			cwd:     "./tools/helper",
			mode:    ModeCode,
			code:    "from pathlib import Path; print(Path.cwd(), open('info.txt').read())",
			wantSub: "/workspace/tools/helper helper-data",
		},
		{
			name:    "tools/helper cwd script",
			cwd:     "tools/helper",
			mode:    ModeScript,
			script:  "run.py",
			wantSub: "/workspace/tools/helper helper-data",
		},
		{
			name:    "tools/helper cwd unittest module discovery",
			cwd:     "tools/helper",
			mode:    ModeModule,
			module:  "unittest",
			args:    []string{"discover", "-v"},
			wantSub: "OK",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			res, err := rt.Execute(ctx, Invocation{
				ExecutionID: "cwd-" + tc.name,
				Mode:        tc.mode,
				Code:        tc.code,
				Script:      tc.script,
				Module:      tc.module,
				CWD:         tc.cwd,
				Args:        tc.args,
				Stdout:      &stdout,
				Stderr:      &stderr,
				Workspace:   newMemWorkspaceAdapter(files),
				Limits:      ResourceLimits{Timeout: 30 * time.Second},
			})
			testutil.FailErr(t, "execute "+tc.name, err)
			combined := stdout.String() + "\n" + stderr.String()
			if !res.CleanCompletion || res.ExitCode != 0 {
				t.Fatalf("%s failed: exit=%d clean=%v out=%q err=%q", tc.name, res.ExitCode, res.CleanCompletion, stdout.String(), stderr.String())
			}
			if !strings.Contains(combined, tc.wantSub) {
				t.Fatalf("%s output %q missing %q", tc.name, combined, tc.wantSub)
			}
		})
	}
}

func TestCPythonWASIDefaultPytest(t *testing.T) {
	rt, _, _ := loadRealCPythonRuntime(t, true)
	ctx := context.Background()

	testFile := `import logging
def test_fixtures(tmp_path, monkeypatch, caplog):
    p = tmp_path / "f.txt"
    p.write_text("ok")
    assert p.read_text() == "ok"
    monkeypatch.setenv("PW_TEST_VAR", "1")
    with caplog.at_level(logging.INFO):
        logging.getLogger("pw").info("hello-caplog")
    assert "hello-caplog" in caplog.text
`
	files := map[string][]byte{
		"test_sample.py": []byte(testFile),
	}

	t.Run("ModeModule pytest with zero extra flags", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		res, err := rt.Execute(ctx, Invocation{
			ExecutionID: "pytest-module-default",
			Mode:        ModeModule,
			Module:      "pytest",
			Stdout:      &stdout,
			Stderr:      &stderr,
			Workspace:   newMemWorkspaceAdapter(files),
			Limits:      ResourceLimits{Timeout: 60 * time.Second},
		})
		testutil.FailErr(t, "execute module pytest", err)
		if !res.CleanCompletion || res.ExitCode != 0 {
			t.Fatalf("pytest module failed: exit=%d clean=%v out=%q err=%q", res.ExitCode, res.CleanCompletion, stdout.String(), stderr.String())
		}
		if !strings.Contains(stdout.String(), "1 passed") {
			t.Fatalf("expected '1 passed' in stdout, got %q", stdout.String())
		}
	})

	t.Run("pytest.main() via import hook with zero extra flags", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		res, err := rt.Execute(ctx, Invocation{
			ExecutionID: "pytest-main-default",
			Mode:        ModeCode,
			Code:        "import pytest\nrc = pytest.main(['test_sample.py'])\nassert rc == 0, f'rc={rc}'\nprint('PYTEST_MAIN_OK')\n",
			Stdout:      &stdout,
			Stderr:      &stderr,
			Workspace:   newMemWorkspaceAdapter(files),
			Limits:      ResourceLimits{Timeout: 60 * time.Second},
		})
		testutil.FailErr(t, "execute pytest.main", err)
		if !res.CleanCompletion || res.ExitCode != 0 {
			t.Fatalf("pytest.main failed: exit=%d clean=%v out=%q err=%q", res.ExitCode, res.CleanCompletion, stdout.String(), stderr.String())
		}
		if !strings.Contains(stdout.String(), "PYTEST_MAIN_OK") {
			t.Fatalf("expected PYTEST_MAIN_OK, got %q", stdout.String())
		}
	})
}

func TestCPythonWASIInterruptibleSleep(t *testing.T) {
	rt, _, _ := loadRealCPythonRuntime(t, true)
	ctx := context.Background()

	t.Run("short time.sleep succeeds", func(t *testing.T) {
		var stdout bytes.Buffer
		res, err := rt.Execute(ctx, Invocation{
			ExecutionID: "sleep-short",
			Mode:        ModeCode,
			Code:        "import time\ntime.sleep(0.05)\nprint('SLEPT_OK')\n",
			Stdout:      &stdout,
			Workspace:   newMemWorkspaceAdapter(nil),
			Limits:      ResourceLimits{Timeout: 30 * time.Second},
		})
		testutil.FailErr(t, "execute short sleep", err)
		if !res.CleanCompletion || res.ExitCode != 0 {
			t.Fatalf("short sleep failed: exit=%d clean=%v stderr=%q", res.ExitCode, res.CleanCompletion, res.GuestStderr)
		}
		if strings.TrimSpace(stdout.String()) != "SLEPT_OK" {
			t.Fatalf("stdout = %q, want SLEPT_OK", stdout.String())
		}
	})

	t.Run("long time.sleep interrupted promptly on cancellation", func(t *testing.T) {
		start := time.Now()
		res, err := rt.Execute(ctx, Invocation{
			ExecutionID: "sleep-cancel",
			Mode:        ModeCode,
			Code:        "import time\ntime.sleep(30)\n",
			Workspace:   newMemWorkspaceAdapter(nil),
			Limits:      ResourceLimits{Timeout: 400 * time.Millisecond},
		})
		elapsed := time.Since(start)
		testutil.FailErr(t, "execute interrupted sleep", err)
		if res.CleanCompletion {
			t.Fatal("interrupted sleep must NOT report CleanCompletion=true")
		}
		if res.Stop == nil || res.Stop.Code != StopCancelled {
			t.Fatalf("expected StopCancelled, got %+v", res.Stop)
		}
		if elapsed > 5*time.Second {
			t.Fatalf("interrupted sleep took %v, expected prompt cancellation", elapsed)
		}
	})
}
