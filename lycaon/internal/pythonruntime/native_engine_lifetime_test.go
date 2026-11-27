package pythonruntime_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/portablepython"
	"github.com/lycaon/lycaon/internal/pythondocuments"
	"github.com/lycaon/lycaon/internal/pythonruntime"
)

func resolveLiveNativeFixtures(t *testing.T) (imageDir, engineDir, importsJSONPath string, manifest *pythonruntime.ImageManifest, wasmBytes []byte) {
	t.Helper()
	imageDir = strings.TrimSpace(os.Getenv("PW_PORTABLE_PYTHON_IMAGE_DIR"))
	engineDir = strings.TrimSpace(os.Getenv("PW_PORTABLE_PYTHON_ENGINE_DIR"))
	importsJSONPath = strings.TrimSpace(os.Getenv("PW_PORTABLE_PYTHON_IMPORTS_JSON"))
	if imageDir == "" || engineDir == "" {
		t.Skip("skipping live native engine test: set PW_PORTABLE_PYTHON_IMAGE_DIR and PW_PORTABLE_PYTHON_ENGINE_DIR")
	}
	var err error
	manifest, err = pythonruntime.LoadImageManifest(filepath.Join(imageDir, "image-manifest.json"))
	if err != nil {
		t.Fatalf("LoadImageManifest(%s): %v", imageDir, err)
	}
	wasmPath, _, _, err := pythonruntime.ResolveImagePaths(imageDir, manifest)
	if err != nil {
		t.Fatalf("ResolveImagePaths(%s): %v", imageDir, err)
	}
	wasmBytes, err = os.ReadFile(wasmPath)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", wasmPath, err)
	}
	return imageDir, engineDir, importsJSONPath, manifest, wasmBytes
}

func policyWithDenied(base []pythonruntime.WasmImportSpec, name string) []pythonruntime.WasmImportSpec {
	out := make([]pythonruntime.WasmImportSpec, len(base))
	copy(out, base)
	for i := range out {
		if out[i].Name == name {
			out[i].Disposition = pythonruntime.ImportDenied
		}
	}
	return out
}

func policyWithout(base []pythonruntime.WasmImportSpec, name string) []pythonruntime.WasmImportSpec {
	out := make([]pythonruntime.WasmImportSpec, 0, len(base))
	for _, spec := range base {
		if spec.Name == name {
			continue
		}
		out = append(out, spec)
	}
	return out
}

func TestImportAuditObservedAndDispositions(t *testing.T) {
	basePolicy := pythonruntime.DefaultWASIImportPolicy()
	observed := []pythonruntime.WasmImportSpec{
		{Module: "wasi_snapshot_preview1", Name: "fd_write", Kind: "func"},
		{Module: "wasi_snapshot_preview1", Name: "path_open", Kind: "func"},
		{Module: "wasi_snapshot_preview1", Name: "sock_accept", Kind: "func"},
	}

	// Default policy: all 3 observed imports are allowed or mediated.
	okRep := pythonruntime.AuditObservedImports(observed, basePolicy)
	if !okRep.Passed {
		t.Fatalf("expected default policy audit to pass; denied=%v undeclared=%v", okRep.Denied, okRep.Undeclared)
	}
	if len(okRep.Observed) != 3 || len(okRep.Allowed) != 1 || len(okRep.Mediated) != 2 {
		t.Fatalf("unexpected audit counts: observed=%d allowed=%d mediated=%d", len(okRep.Observed), len(okRep.Allowed), len(okRep.Mediated))
	}

	// Denied case: mark sock_accept as ImportDenied.
	deniedRep := pythonruntime.AuditObservedImports(observed, policyWithDenied(basePolicy, "sock_accept"))
	if deniedRep.Passed {
		t.Fatal("expected audit with denied sock_accept to fail")
	}
	if len(deniedRep.Denied) != 1 || deniedRep.Denied[0].Name != "sock_accept" {
		t.Fatalf("expected denied=[sock_accept], got %+v", deniedRep.Denied)
	}
	if len(deniedRep.Undeclared) != 0 {
		t.Fatalf("expected 0 undeclared, got %+v", deniedRep.Undeclared)
	}

	// Undeclared case: omit fd_write from policy.
	undeclRep := pythonruntime.AuditObservedImports(observed, policyWithout(basePolicy, "fd_write"))
	if undeclRep.Passed {
		t.Fatal("expected audit with undeclared fd_write to fail")
	}
	if len(undeclRep.Undeclared) != 1 || undeclRep.Undeclared[0].Name != "fd_write" {
		t.Fatalf("expected undeclared=[fd_write], got %+v", undeclRep.Undeclared)
	}
	if len(undeclRep.Denied) != 0 {
		t.Fatalf("expected 0 denied, got %+v", undeclRep.Denied)
	}
}

func TestNativeEngineImportAuditLive(t *testing.T) {
	ctx := context.Background()
	imageDir, engineDir, importsJSONPath, manifest, wasmBytes := resolveLiveNativeFixtures(t)

	// 1. Default policy succeeds and matches both wazero's compiled import audit
	// and the preserved PY07 imports.json inventory (exact 43 imports, NOT the
	// 44-entry DefaultWASIImportPolicy table).
	rt, err := pythonruntime.NewNativeEngineRuntime(ctx, pythonruntime.RuntimeConfig{
		Backend:             pythonruntime.BackendNativeAOT,
		EngineDir:           engineDir,
		WasmBytes:           wasmBytes,
		ImageDir:            imageDir,
		Manifest:            manifest,
		EnforceImportAudit:  true,
		UseTrustedBootstrap: true,
		ScratchRoot:         t.TempDir(),
	})
	if err != nil {
		t.Fatalf("NewNativeEngineRuntime(default policy): %v", err)
	}
	defer func() { _ = rt.Close(ctx) }()

	rep := rt.ImportAudit()
	if rep == nil || !rep.Passed {
		t.Fatalf("expected passing ImportAuditReport, got %+v", rep)
	}
	if len(rep.Observed) != int(pythonruntime.ExpectedNativeEngineImportCount) {
		t.Fatalf("observed import count = %d, want %d (must reflect actual module imports, not policy table)",
			len(rep.Observed), pythonruntime.ExpectedNativeEngineImportCount)
	}
	if len(rep.Allowed) == 0 || len(rep.Mediated) == 0 || len(rep.Denied) != 0 || len(rep.Undeclared) != 0 {
		t.Fatalf("unexpected disposition breakdown: allowed=%d mediated=%d denied=%d undeclared=%d",
			len(rep.Allowed), len(rep.Mediated), len(rep.Denied), len(rep.Undeclared))
	}
	if len(rep.Allowed)+len(rep.Mediated) != len(rep.Observed) {
		t.Fatalf("allowed (%d) + mediated (%d) != observed (%d)", len(rep.Allowed), len(rep.Mediated), len(rep.Observed))
	}

	// Cross-check exact equality against wazero's AuditModuleImports.
	wazeroRep, err := pythonruntime.AuditModuleImports(ctx, wasmBytes, pythonruntime.DefaultWASIImportPolicy())
	if err != nil {
		t.Fatalf("AuditModuleImports: %v", err)
	}
	if len(rep.Observed) != len(wazeroRep.Observed) {
		t.Fatalf("native observed (%d) != wazero observed (%d)", len(rep.Observed), len(wazeroRep.Observed))
	}
	for i := range rep.Observed {
		if rep.Observed[i].Module != wazeroRep.Observed[i].Module || rep.Observed[i].Name != wazeroRep.Observed[i].Name {
			t.Fatalf("observed[%d] mismatch: native=%+v wazero=%+v", i, rep.Observed[i], wazeroRep.Observed[i])
		}
	}

	// Cross-check against preserved imports.json if provided or present in engineDir.
	if importsJSONPath == "" {
		cand := filepath.Join(engineDir, "imports.json")
		if info, sErr := os.Stat(cand); sErr == nil && !info.IsDir() {
			importsJSONPath = cand
		}
	}
	if importsJSONPath != "" {
		inv, err := pythonruntime.LoadVerifiedImportInventory(
			importsJSONPath,
			pythonruntime.ExpectedNativeEngineImportInventorySHA256,
			pythonruntime.ExpectedNativeEngineImportCount,
		)
		if err != nil {
			t.Fatalf("LoadVerifiedImportInventory(%s): %v", importsJSONPath, err)
		}
		if len(inv) != len(rep.Observed) {
			t.Fatalf("preserved imports.json count (%d) != native audit observed (%d)", len(inv), len(rep.Observed))
		}
		for i := range inv {
			if inv[i].Module != rep.Observed[i].Module || inv[i].Name != rep.Observed[i].Name {
				t.Fatalf("imports.json[%d] mismatch: inv=%+v native=%+v", i, inv[i], rep.Observed[i])
			}
		}
	}

	// 2. Denied import case: fail closed when EnforceImportAudit=true, and report
	// Passed=false + Denied=[sock_accept] when EnforceImportAudit=false.
	deniedPolicy := policyWithDenied(pythonruntime.DefaultWASIImportPolicy(), "sock_accept")
	if _, err := pythonruntime.NewNativeEngineRuntime(ctx, pythonruntime.RuntimeConfig{
		Backend:            pythonruntime.BackendNativeAOT,
		EngineDir:          engineDir,
		WasmBytes:          wasmBytes,
		ImageDir:           imageDir,
		Manifest:           manifest,
		ImportPolicy:       deniedPolicy,
		EnforceImportAudit: true,
		ScratchRoot:        t.TempDir(),
	}); err == nil {
		t.Fatal("expected NewNativeEngineRuntime to fail closed when sock_accept is denied")
	}
	rtDenied, err := pythonruntime.NewNativeEngineRuntime(ctx, pythonruntime.RuntimeConfig{
		Backend:            pythonruntime.BackendNativeAOT,
		EngineDir:          engineDir,
		WasmBytes:          wasmBytes,
		ImageDir:           imageDir,
		Manifest:           manifest,
		ImportPolicy:       deniedPolicy,
		EnforceImportAudit: false,
		ScratchRoot:        t.TempDir(),
	})
	if err != nil {
		t.Fatalf("NewNativeEngineRuntime(EnforceImportAudit=false, denied): %v", err)
	}
	defer func() { _ = rtDenied.Close(ctx) }()
	if rtDenied.ImportAudit().Passed {
		t.Fatal("expected ImportAudit().Passed == false for denied sock_accept")
	}
	if len(rtDenied.ImportAudit().Denied) != 1 || rtDenied.ImportAudit().Denied[0].Name != "sock_accept" {
		t.Fatalf("expected Denied=[sock_accept], got %+v", rtDenied.ImportAudit().Denied)
	}

	// 3. Undeclared import case: fail closed when EnforceImportAudit=true, and
	// report Passed=false + Undeclared=[fd_write] when EnforceImportAudit=false.
	undeclaredPolicy := policyWithout(pythonruntime.DefaultWASIImportPolicy(), "fd_write")
	if _, err := pythonruntime.NewNativeEngineRuntime(ctx, pythonruntime.RuntimeConfig{
		Backend:            pythonruntime.BackendNativeAOT,
		EngineDir:          engineDir,
		WasmBytes:          wasmBytes,
		ImageDir:           imageDir,
		Manifest:           manifest,
		ImportPolicy:       undeclaredPolicy,
		EnforceImportAudit: true,
		ScratchRoot:        t.TempDir(),
	}); err == nil {
		t.Fatal("expected NewNativeEngineRuntime to fail closed when fd_write is undeclared")
	}
	rtUndecl, err := pythonruntime.NewNativeEngineRuntime(ctx, pythonruntime.RuntimeConfig{
		Backend:            pythonruntime.BackendNativeAOT,
		EngineDir:          engineDir,
		WasmBytes:          wasmBytes,
		ImageDir:           imageDir,
		Manifest:           manifest,
		ImportPolicy:       undeclaredPolicy,
		EnforceImportAudit: false,
		ScratchRoot:        t.TempDir(),
	})
	if err != nil {
		t.Fatalf("NewNativeEngineRuntime(EnforceImportAudit=false, undeclared): %v", err)
	}
	defer func() { _ = rtUndecl.Close(ctx) }()
	if rtUndecl.ImportAudit().Passed {
		t.Fatal("expected ImportAudit().Passed == false for undeclared fd_write")
	}
	if len(rtUndecl.ImportAudit().Undeclared) != 1 || rtUndecl.ImportAudit().Undeclared[0].Name != "fd_write" {
		t.Fatalf("expected Undeclared=[fd_write], got %+v", rtUndecl.ImportAudit().Undeclared)
	}
}

func newLiveNativeService(t *testing.T, imageDir, engineDir string) (*portablepython.Service, string) {
	t.Helper()
	wsDir := t.TempDir()
	svc, err := portablepython.NewService(context.Background(), portablepython.ServiceConfig{
		ImageDir:  imageDir,
		EngineDir: engineDir,
		Backend:   pythonruntime.BackendNativeAOT,
	})
	if err != nil {
		t.Fatalf("portablepython.NewService(native-aot): %v", err)
	}
	if info := svc.RuntimeInfo(); info.Backend != pythonruntime.BackendNativeAOT {
		_ = svc.Close(context.Background())
		t.Fatalf("expected BackendNativeAOT, got %s", info.Backend)
	}
	return svc, wsDir
}

func TestNativeEngineServiceLifetimeConcurrentCloseAndDrain(t *testing.T) {
	imageDir, engineDir, _, _, _ := resolveLiveNativeFixtures(t)
	ctx := context.Background()

	t.Run("ConcurrentCloseDuringCPULoop", func(t *testing.T) {
		svc, wsDir := newLiveNativeService(t, imageDir, engineDir)

		started := make(chan struct{})
		done := make(chan struct{})
		var execRes *portablepython.ToolResponse
		var execErr error

		go func() {
			defer close(done)
			close(started)
			execRes, execErr = svc.ExecuteTool(ctx, portablepython.ToolRequest{
				Mode: pythonruntime.ModeCode,
				Code: "print('LOOP_READY', flush=True)\nwhile True:\n    pass\n",
				Roots: []pythondocuments.HostRootConfig{
					{ID: "workspace", Dir: wsDir},
				},
				Limits: pythonruntime.ResourceLimits{
					MaxMemoryPages: 4096,
					MaxGuestWork:   100_000_000_000,
					Timeout:        30 * time.Second,
				},
			})
		}()

		<-started
		// Allow CPython to boot into the infinite CPU loop before closing.
		time.Sleep(120 * time.Millisecond)

		closeStart := time.Now()
		if err := svc.Close(ctx); err != nil {
			t.Fatalf("svc.Close returned error: %v", err)
		}
		closeDur := time.Since(closeStart)
		if closeDur > 5*time.Second {
			t.Fatalf("svc.Close took too long to cancel and drain CPU loop: %v", closeDur)
		}

		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("ExecuteTool goroutine did not finish after svc.Close returned")
		}
		if execErr == nil && execRes != nil && execRes.CleanCompletion {
			t.Fatalf("expected canceled/non-clean outcome on concurrent Close, got CleanCompletion=true")
		}
	})

	t.Run("ConcurrentCloseAndContextCancelRace", func(t *testing.T) {
		svc, wsDir := newLiveNativeService(t, imageDir, engineDir)

		const workers = 3
		cancels := make([]context.CancelFunc, workers)
		var wg sync.WaitGroup
		wg.Add(workers)
		for i := 0; i < workers; i++ {
			callCtx, cancel := context.WithCancel(ctx)
			cancels[i] = cancel
			go func(c context.Context) {
				defer wg.Done()
				_, _ = svc.ExecuteTool(c, portablepython.ToolRequest{
					Mode: pythonruntime.ModeCode,
					Code: "while True:\n    pass\n",
					Roots: []pythondocuments.HostRootConfig{
						{ID: "workspace", Dir: wsDir},
					},
					Limits: pythonruntime.ResourceLimits{
						MaxMemoryPages: 4096,
						MaxGuestWork:   100_000_000_000,
						Timeout:        30 * time.Second,
					},
				})
			}(callCtx)
		}

		time.Sleep(100 * time.Millisecond)

		var closeWG sync.WaitGroup
		closeWG.Add(2)
		go func() {
			defer closeWG.Done()
			for _, c := range cancels {
				c()
			}
		}()
		go func() {
			defer closeWG.Done()
			if err := svc.Close(ctx); err != nil {
				t.Errorf("svc.Close error: %v", err)
			}
		}()
		closeWG.Wait()
		wg.Wait()
	})

	t.Run("RepeatCloseConcurrentAndSequential", func(t *testing.T) {
		svc, _ := newLiveNativeService(t, imageDir, engineDir)

		const closers = 6
		var wg sync.WaitGroup
		wg.Add(closers)
		for i := 0; i < closers; i++ {
			go func() {
				defer wg.Done()
				if err := svc.Close(ctx); err != nil {
					t.Errorf("concurrent svc.Close error: %v", err)
				}
			}()
		}
		wg.Wait()

		// Sequential repeat Close calls must also be idempotent.
		for i := 0; i < 2; i++ {
			if err := svc.Close(ctx); err != nil {
				t.Fatalf("sequential repeat svc.Close[%d] error: %v", i, err)
			}
		}
	})

	t.Run("ExecuteAfterCloseFailsCleanly", func(t *testing.T) {
		svc, wsDir := newLiveNativeService(t, imageDir, engineDir)

		// Verify a normal call succeeds before Close.
		res, err := svc.ExecuteTool(ctx, portablepython.ToolRequest{
			Mode: pythonruntime.ModeCode,
			Code: "print('before-close-ok')",
			Roots: []pythondocuments.HostRootConfig{
				{ID: "workspace", Dir: wsDir},
			},
		})
		if err != nil || !res.CleanCompletion || !strings.Contains(res.Stdout, "before-close-ok") {
			t.Fatalf("pre-close ExecuteTool failed: res=%+v err=%v", res, err)
		}

		if err := svc.Close(ctx); err != nil {
			t.Fatalf("svc.Close: %v", err)
		}

		// Direct runtime Execute after Close must return ErrRuntimeClosed.
		var out bytes.Buffer
		_, rtErr := svc.Runtime().Execute(ctx, pythonruntime.Invocation{
			Mode:   pythonruntime.ModeCode,
			Code:   "print('must-not-run')",
			Stdout: &out,
		})
		if !errors.Is(rtErr, pythonruntime.ErrRuntimeClosed) {
			t.Fatalf("Runtime.Execute after Close: got err=%v, want ErrRuntimeClosed", rtErr)
		}

		// Service.ExecuteTool after Close must return a clean error and never touch unloaded lib.
		_, svcErr := svc.ExecuteTool(ctx, portablepython.ToolRequest{
			Mode: pythonruntime.ModeCode,
			Code: "print('must-not-run')",
			Roots: []pythondocuments.HostRootConfig{
				{ID: "workspace", Dir: wsDir},
			},
		})
		if svcErr == nil {
			t.Fatal("Service.ExecuteTool after Close unexpectedly succeeded")
		}
		if !errors.Is(svcErr, pythonruntime.ErrRuntimeClosed) && !strings.Contains(svcErr.Error(), "closed") {
			t.Fatalf("Service.ExecuteTool after Close returned unexpected error: %v", svcErr)
		}
	})

	t.Run("ConcurrentExecuteAndCloseAdmissionRace", func(t *testing.T) {
		svc, wsDir := newLiveNativeService(t, imageDir, engineDir)
		var admitted atomic.Int64
		var rejected atomic.Int64
		var wg sync.WaitGroup

		for i := 0; i < 6; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				res, err := svc.ExecuteTool(ctx, portablepython.ToolRequest{
					Mode: pythonruntime.ModeCode,
					Code: "print(sum(range(1000)))",
					Roots: []pythondocuments.HostRootConfig{
						{ID: "workspace", Dir: wsDir},
					},
				})
				if err != nil {
					rejected.Add(1)
					return
				}
				if res != nil {
					admitted.Add(1)
				}
			}()
		}
		time.Sleep(15 * time.Millisecond)
		if err := svc.Close(ctx); err != nil {
			t.Fatalf("svc.Close: %v", err)
		}
		wg.Wait()
		t.Logf("admission race summary: admitted=%d rejected=%d", admitted.Load(), rejected.Load())
	})
}
