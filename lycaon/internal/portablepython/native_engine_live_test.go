package portablepython_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/portablepython"
	"github.com/lycaon/lycaon/internal/pythonadmission"
	"github.com/lycaon/lycaon/internal/pythondocuments"
	"github.com/lycaon/lycaon/internal/pythonruntime"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
)

func requireLiveNativeEngineEnv(t *testing.T) (imageDir, engineDir string) {
	t.Helper()
	engineDir = strings.TrimSpace(os.Getenv(pythonruntime.EnvEngineDir))
	if engineDir == "" {
		t.Skipf("skipping live native engine test: %s not set", pythonruntime.EnvEngineDir)
	}
	imageDir = strings.TrimSpace(os.Getenv(portablepython.EnvImageDir))
	if imageDir == "" {
		var err error
		imageDir, err = portablepython.DiscoverImageDir()
		if err != nil {
			t.Skipf("skipping live native engine test: %v", err)
		}
	}
	return imageDir, engineDir
}

func TestNativeEngineLiveExecutionPolicyFuelAndCancel(t *testing.T) {
	imageDir, engineDir := requireLiveNativeEngineEnv(t)
	ctx := context.Background()

	// Verify default backend selection (empty Backend + empty EngineDir discovered
	// via PW_PORTABLE_PYTHON_ENGINE_DIR) selects BackendNativeAOT on BOTH
	// darwin/arm64 and linux/amd64 without silent fallback.
	if got := pythonruntime.DefaultBackendKind(); got != pythonruntime.BackendNativeAOT {
		t.Fatalf("DefaultBackendKind()=%q want %q on %s/%s", got, pythonruntime.BackendNativeAOT, runtime.GOOS, runtime.GOARCH)
	}
	svc, err := portablepython.NewService(ctx, portablepython.ServiceConfig{
		ImageDir: imageDir,
	})
	if err != nil {
		t.Fatalf("NewService(default native backend) failed: %v", err)
	}
	defer func() { _ = svc.Close(ctx) }()

	info := svc.RuntimeInfo()
	if info.Backend != pythonruntime.BackendNativeAOT {
		t.Fatalf("RuntimeInfo.Backend=%q want %q", info.Backend, pythonruntime.BackendNativeAOT)
	}
	if info.NativeEngine == nil || info.NativeEngine.EngineTag != "pw-cpython-wasi-engine-v0.2.0" {
		t.Fatalf("unexpected NativeEngine identity: %+v", info.NativeEngine)
	}
	t.Logf("verified native engine: tag=%s platform=%s commit=%s abi=%d",
		info.NativeEngine.EngineTag, info.NativeEngine.Platform, info.NativeEngine.SourceCommit, info.NativeEngine.ABIVersion)

	// 1. Cold & warm workbook execution (PyYAML + openpyxl + workspace settlement).
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "repos.yaml"), []byte("repos:\n  - name: alpha\n    stars: 42\n  - name: beta\n    stars: 99\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "blocked.txt"), []byte("TOP_SECRET_VALUE"), 0o644); err != nil {
		t.Fatal(err)
	}
	roots := []pythondocuments.HostRootConfig{{ID: "ws", Dir: ws}}
	door := native.NewPythonMutationDoor(tools.ToolContext{}, roots)
	allowAll := func(_ context.Context, _ pythonruntime.ReachedOperation) pythonadmission.PolicyDecision {
		return pythonadmission.PolicyDecision{Kind: pythonruntime.DecisionAllow}
	}

	workbookCode := `
import sys, yaml, openpyxl
data = yaml.safe_load(open("repos.yaml", "r", encoding="utf-8"))
wb = openpyxl.Workbook()
ws = wb.active
ws.title = "Repos"
ws.freeze_panes = "A2"
ws.append(["name", "stars"])
for r in data["repos"]:
    ws.append([r["name"], r["stars"]])
wb.save("out.xlsx")

wb2 = openpyxl.load_workbook("out.xlsx")
ws2 = wb2["Repos"]
assert ws2.freeze_panes == "A2"
assert ws2.max_row == 3
print(f"OK:{sys.version_info.major}.{sys.version_info.minor}:rows={ws2.max_row}")
`
	for _, label := range []string{"cold", "warm"} {
		t0 := time.Now()
		res, err := svc.ExecuteTool(ctx, portablepython.ToolRequest{
			ExecutionID:  "native-wb-" + label,
			Mode:         pythonruntime.ModeCode,
			Code:         workbookCode,
			Roots:        roots,
			MutationDoor: door,
			Policy:       allowAll,
		})
		elapsed := time.Since(t0)
		if err != nil {
			t.Fatalf("%s workbook execution error: %v", label, err)
		}
		if !res.CleanCompletion || res.ExitCode != 0 {
			t.Fatalf("%s workbook failed: exit=%d clean=%v stop=%+v stdout=%q stderr=%q diag=%v settlement=%+v",
				label, res.ExitCode, res.CleanCompletion, res.Stop, res.Stdout, res.Stderr, res.RuntimeDiagnostics, res.Settlement)
		}
		if !strings.Contains(res.Stdout, "OK:3.12:rows=3") {
			t.Fatalf("%s unexpected stdout: %q", label, res.Stdout)
		}
		if res.Backend != pythonruntime.BackendNativeAOT {
			t.Fatalf("%s expected backend=%q got %q", label, pythonruntime.BackendNativeAOT, res.Backend)
		}
		t.Logf("native %s workbook: %s (guest_work=%d host_work=%d peak_pages=%d)",
			label, elapsed.Round(time.Millisecond), res.Usage.GuestWork, res.Usage.HostWork, res.Usage.PeakMemoryPages)
	}
	if _, err := os.Stat(filepath.Join(ws, "out.xlsx")); err != nil {
		t.Fatalf("expected settled out.xlsx in workspace: %v", err)
	}

	// 2. Policy denial on blocked.txt: must stop with COMMAND_EFFECT_NOT_ALLOWED and disclose nothing.
	denyPolicy := func(_ context.Context, op pythonruntime.ReachedOperation) pythonadmission.PolicyDecision {
		if op.Family == pythonruntime.EffectFilesystemRead && strings.HasSuffix(op.Target.RelPath, "blocked.txt") {
			return pythonadmission.PolicyDecision{
				Kind:   pythonruntime.DecisionStop,
				Reason: "blocked.txt denied by policy",
			}
		}
		return pythonadmission.PolicyDecision{Kind: pythonruntime.DecisionAllow}
	}
	denyRes, err := svc.ExecuteTool(ctx, portablepython.ToolRequest{
		ExecutionID: "native-deny",
		Mode:        pythonruntime.ModeCode,
		Code:        `print(open("blocked.txt").read())`,
		Roots:       roots,
		Policy:      denyPolicy,
	})
	if err != nil {
		t.Fatalf("policy denial execution error: %v", err)
	}
	if denyRes.CleanCompletion || denyRes.Stop == nil || denyRes.Stop.Code != pythonruntime.StopEffectNotAllowed {
		t.Fatalf("expected StopEffectNotAllowed, got clean=%v stop=%+v", denyRes.CleanCompletion, denyRes.Stop)
	}
	if strings.Contains(denyRes.Stdout, "TOP_SECRET_VALUE") || strings.Contains(denyRes.Stderr, "TOP_SECRET_VALUE") {
		t.Fatalf("secret leaked on denied read: stdout=%q stderr=%q", denyRes.Stdout, denyRes.Stderr)
	}

	// 3. Deterministic fuel exhaustion.
	fuelRes, err := svc.ExecuteTool(ctx, portablepython.ToolRequest{
		ExecutionID: "native-fuel",
		Mode:        pythonruntime.ModeCode,
		Code:        "x = 0\nwhile True:\n    x += 1\n",
		Roots:       roots,
		Policy:      allowAll,
		Limits: pythonruntime.ResourceLimits{
			MaxGuestWork: 20_000_000,
			Timeout:      30 * time.Second,
		},
	})
	if err != nil {
		t.Fatalf("fuel exhaustion execution error: %v", err)
	}
	if fuelRes.CleanCompletion || fuelRes.Stop == nil || fuelRes.Stop.Code != pythonruntime.StopWorkLimitExceeded {
		t.Fatalf("expected StopWorkLimitExceeded, got clean=%v stop=%+v", fuelRes.CleanCompletion, fuelRes.Stop)
	}
	t.Logf("native fuel exhaustion: stop=%s guest_work=%d", fuelRes.Stop.Code, fuelRes.Usage.GuestWork)

	// 4. Asynchronous cancellation of a pure CPU-only loop via context timeout.
	cancelStart := time.Now()
	cancelRes, err := svc.ExecuteTool(ctx, portablepython.ToolRequest{
		ExecutionID: "native-cancel",
		Mode:        pythonruntime.ModeCode,
		Code:        "while True:\n    pass\n",
		Roots:       roots,
		Policy:      allowAll,
		Limits: pythonruntime.ResourceLimits{
			MaxGuestWork: 100_000_000_000,
			Timeout:      400 * time.Millisecond,
		},
	})
	cancelElapsed := time.Since(cancelStart)
	if err != nil {
		t.Fatalf("async cancel execution error: %v", err)
	}
	if cancelRes.CleanCompletion || cancelRes.Stop == nil || cancelRes.Stop.Code != pythonruntime.StopCancelled {
		t.Fatalf("expected StopCancelled on CPU loop timeout, got clean=%v stop=%+v elapsed=%s",
			cancelRes.CleanCompletion, cancelRes.Stop, cancelElapsed)
	}
	if cancelElapsed > 5*time.Second {
		t.Fatalf("async cancel took too long: %s", cancelElapsed)
	}
	t.Logf("native async CPU-loop cancel: stop=%s elapsed=%s", cancelRes.Stop.Code, cancelElapsed.Round(time.Millisecond))

	// 5. Fail-closed tampered library check (ZERO silent fallback to wazero).
	tamperedDir := t.TempDir()
	libName, err := pythonruntime.LibraryNameForPlatform(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	origLib, err := os.ReadFile(filepath.Join(engineDir, libName))
	if err != nil {
		t.Fatal(err)
	}
	origLib[len(origLib)-1] ^= 0xff
	if err := os.WriteFile(filepath.Join(tamperedDir, libName), origLib, 0o755); err != nil {
		t.Fatal(err)
	}
	origMan, err := os.ReadFile(filepath.Join(engineDir, "artifact-manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tamperedDir, "artifact-manifest.json"), origMan, 0o644); err != nil {
		t.Fatal(err)
	}
	_, tamperErr := portablepython.NewService(ctx, portablepython.ServiceConfig{
		ImageDir:  imageDir,
		Backend:   pythonruntime.BackendNativeAOT,
		EngineDir: tamperedDir,
	})
	if tamperErr == nil {
		t.Fatal("expected tampered native library to fail closed before dlopen")
	}
	var ve *pythonruntime.NativeEngineVerificationError
	if !errors.As(tamperErr, &ve) || ve.Stage != "artifact" {
		t.Fatalf("expected NativeEngineVerificationError stage=artifact, got %v", tamperErr)
	}
}
