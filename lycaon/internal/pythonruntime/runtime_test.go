package pythonruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestStopLatchAndSealedWriter(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	latch := NewStopLatch(cancel)
	var dst bytes.Buffer
	sw := NewSealedWriter(&dst, latch, 0)

	n, err := sw.Write([]byte("hello "))
	testutil.FailErr(t, "write before stop", err)
	if n != 6 {
		t.Fatalf("wrote %d bytes, want 6", n)
	}

	first := latch.Trigger(TerminalStop{
		Code:        StopEffectNotAllowed,
		Reason:      "blocked write",
		OperationID: "op-1",
	})
	if !first {
		t.Fatal("first Trigger returned false")
	}
	if second := latch.Trigger(TerminalStop{Code: StopCancelled}); second {
		t.Fatal("second Trigger returned true; first stop must win")
	}
	if ctx.Err() == nil {
		t.Fatal("StopLatch.Trigger did not cancel execution context")
	}
	if !sw.Sealed() {
		t.Fatal("SealedWriter was not sealed by StopLatch.Trigger")
	}
	if got := latch.Stop(); got == nil || got.Code != StopEffectNotAllowed || got.OperationID != "op-1" {
		t.Fatalf("unexpected latched stop: %+v", got)
	}

	n, err = sw.Write([]byte("post-stop bytes"))
	if !errors.Is(err, ErrStreamSealed) {
		t.Fatalf("post-stop write err = %v, want ErrStreamSealed", err)
	}
	if n != 0 {
		t.Fatalf("post-stop write delivered %d bytes, want 0", n)
	}
	if dst.String() != "hello " {
		t.Fatalf("downstream received %q, want %q", dst.String(), "hello ")
	}
	if sw.WrittenBytes() != 6 || sw.DroppedBytes() != 15 {
		t.Fatalf("written=%d dropped=%d, want 6 and 15", sw.WrittenBytes(), sw.DroppedBytes())
	}
}

func TestSealedWriterOutputLimit(t *testing.T) {
	_, cancel := context.WithCancel(context.Background())
	defer cancel()

	latch := NewStopLatch(cancel)
	var dst bytes.Buffer
	sw := NewSealedWriter(&dst, latch, 5)

	n, err := sw.Write([]byte("12345678"))
	if !errors.Is(err, ErrOutputLimitExceeded) {
		t.Fatalf("over-cap write err = %v, want ErrOutputLimitExceeded", err)
	}
	if n != 5 || dst.String() != "12345" {
		t.Fatalf("wrote %d (%q), want 5 (%q)", n, dst.String(), "12345")
	}
	if !latch.Stopped() || latch.Stop().Code != StopOutputLimitExceeded {
		t.Fatalf("expected StopOutputLimitExceeded, got %+v", latch.Stop())
	}
	if _, err := sw.Write([]byte("more")); !errors.Is(err, ErrStreamSealed) {
		t.Fatalf("subsequent write err = %v, want ErrStreamSealed", err)
	}
}

func TestParkedGuestTracker(t *testing.T) {
	tracker := NewParkedGuestTracker(ParkedGuestBudget{
		MaxParkedGuests:        2,
		MaxRetainedMemoryBytes: 100,
		MaxStagedBytes:         50,
		MaxOpenDescriptors:     10,
		MaxReviewLeases:        4,
	})

	err := tracker.Park(ParkedGuestReservation{
		ExecutionID:         "exec-1",
		RetainedMemoryBytes: 60,
		StagedBytes:         20,
		OpenDescriptors:     4,
		ReviewLeases:        2,
	})
	testutil.FailErr(t, "park exec-1", err)

	if err := tracker.Park(ParkedGuestReservation{ExecutionID: "exec-1"}); !errors.Is(err, ErrGuestAlreadyParked) {
		t.Fatalf("duplicate park err = %v, want ErrGuestAlreadyParked", err)
	}

	if err := tracker.Park(ParkedGuestReservation{
		ExecutionID:         "exec-2",
		RetainedMemoryBytes: 50, // 60+50 > 100
	}); !errors.Is(err, ErrParkedBudgetExceeded) {
		t.Fatalf("over-memory park err = %v, want ErrParkedBudgetExceeded", err)
	}

	err = tracker.Park(ParkedGuestReservation{
		ExecutionID:         "exec-2",
		RetainedMemoryBytes: 40,
		StagedBytes:         30,
		OpenDescriptors:     6,
		ReviewLeases:        2,
	})
	testutil.FailErr(t, "park exec-2", err)

	if err := tracker.Park(ParkedGuestReservation{ExecutionID: "exec-3"}); !errors.Is(err, ErrParkedBudgetExceeded) {
		t.Fatalf("over-count park err = %v, want ErrParkedBudgetExceeded", err)
	}

	snap := tracker.Snapshot()
	if snap.ParkedGuests != 2 || snap.RetainedMemoryBytes != 100 || snap.StagedBytes != 50 || snap.OpenDescriptors != 10 || snap.ReviewLeases != 4 {
		t.Fatalf("unexpected snapshot: %+v", snap)
	}

	if !tracker.Release("exec-1") {
		t.Fatal("Release(exec-1) returned false")
	}
	if tracker.Release("exec-1") {
		t.Fatal("second Release(exec-1) returned true")
	}
	snap = tracker.Snapshot()
	if snap.ParkedGuests != 1 || snap.RetainedMemoryBytes != 40 {
		t.Fatalf("unexpected snapshot after release: %+v", snap)
	}

	if released := tracker.ReleaseAll(); released != 1 {
		t.Fatalf("ReleaseAll = %d, want 1", released)
	}
	if snap := tracker.Snapshot(); snap != (ParkedGuestSnapshot{}) {
		t.Fatalf("expected zero snapshot after ReleaseAll, got %+v", snap)
	}
}

func TestImageManifestLoadAndVerify(t *testing.T) {
	dir := t.TempDir()
	wasmPath := filepath.Join(dir, "python.wasm")
	stdlibDir := filepath.Join(dir, "stdlib")
	packagesDir := filepath.Join(dir, "packages")

	testutil.FailErr(t, "mkdir stdlib", os.MkdirAll(stdlibDir, 0o755))
	testutil.FailErr(t, "mkdir packages", os.MkdirAll(packagesDir, 0o755))
	testutil.FailErr(t, "write wasm", os.WriteFile(wasmPath, wasmFreshCounterModule(), 0o644))
	testutil.FailErr(t, "write stdlib file", os.WriteFile(filepath.Join(stdlibDir, "os.py"), []byte("# stdlib\n"), 0o644))
	testutil.FailErr(t, "write pkg file", os.WriteFile(filepath.Join(packagesDir, "demo.py"), []byte("X = 1\n"), 0o644))

	wasmHash, err := HashFile(wasmPath)
	testutil.FailErr(t, "hash wasm", err)
	stdlibHash, err := HashTree(stdlibDir)
	testutil.FailErr(t, "hash stdlib", err)
	pkgTreeHash, err := HashTree(packagesDir)
	testutil.FailErr(t, "hash packages tree", err)

	manifest := &ImageManifest{
		SchemaVersion: ImageManifestSchemaVersion,
		ImageID:       "py312-wasi-test",
		BuiltAt:       "2026-11-26T00:00:00Z",
		Target:        "wasm32-wasi",
		Interpreter: InterpreterSpec{
			Name:         "cpython",
			Version:      "3.12.0",
			WasmPath:     "python.wasm",
			WasmSHA256:   wasmHash,
			StdlibPath:   "stdlib",
			StdlibSHA256: stdlibHash,
		},
		PackagesRoot:       "packages",
		PackagesTreeSHA256: pkgTreeHash,
		Distributions: []DistributionSpec{
			{
				Name:           "demo",
				NormalizedName: "demo",
				Version:        "1.0.0",
				Public:         true,
				Imports:        []string{"demo"},
			},
		},
	}

	manifestPath := filepath.Join(dir, "manifest.json")
	raw, err := json.MarshalIndent(manifest, "", "  ")
	testutil.FailErr(t, "marshal manifest", err)
	testutil.FailErr(t, "write manifest", os.WriteFile(manifestPath, raw, 0o644))

	loaded, err := LoadImageManifest(manifestPath)
	testutil.FailErr(t, "load manifest", err)
	testutil.FailErr(t, "verify clean image", VerifyImage(dir, loaded))

	// Unlisted file under packages/ must fail packages_tree_sha256 verification.
	extraPath := filepath.Join(packagesDir, "unlisted.py")
	testutil.FailErr(t, "write unlisted pkg file", os.WriteFile(extraPath, []byte("EVIL = 1\n"), 0o644))
	if err := VerifyImage(dir, loaded); err == nil || !strings.Contains(err.Error(), "packages_root tree sha256 mismatch") {
		t.Fatalf("expected packages_root tree sha256 mismatch, got %v", err)
	}
	testutil.FailErr(t, "remove unlisted pkg file", os.Remove(extraPath))

	// Native binary injection must fail verification.
	soPath := filepath.Join(dir, "bad.so")
	testutil.FailErr(t, "write native binary", os.WriteFile(soPath, []byte("ELF"), 0o644))
	if err := VerifyImage(dir, loaded); err == nil || !strings.Contains(err.Error(), "native binaries forbidden") {
		t.Fatalf("expected native binary rejection, got %v", err)
	}
	testutil.FailErr(t, "remove native binary", os.Remove(soPath))

	// Tampered wasm must fail digest check.
	testutil.FailErr(t, "tamper wasm", os.WriteFile(wasmPath, []byte("corrupted"), 0o644))
	if err := VerifyImage(dir, loaded); err == nil || !strings.Contains(err.Error(), "wasm sha256 mismatch") {
		t.Fatalf("expected wasm sha256 mismatch, got %v", err)
	}
}

func TestImportAudit(t *testing.T) {
	ctx := context.Background()
	policy := DefaultWASIImportPolicy()

	allowedReport, err := AuditModuleImports(ctx, wasmModuleWithImport("wasi_snapshot_preview1", "proc_exit"), policy)
	testutil.FailErr(t, "audit allowed", err)
	if !allowedReport.Passed || len(allowedReport.Allowed) != 1 {
		t.Fatalf("expected allowed pass, got %+v", allowedReport)
	}

	sockReport, err := AuditModuleImports(ctx, wasmModuleWithImport("wasi_snapshot_preview1", "sock_accept"), policy)
	testutil.FailErr(t, "audit mediated socket stub", err)
	if !sockReport.Passed || len(sockReport.Mediated) != 1 || sockReport.Mediated[0].Authority != "none" {
		t.Fatalf("expected mediated no-authority sock_accept, got %+v", sockReport)
	}

	deniedReport, err := AuditModuleImports(ctx, wasmModuleWithImport("wasi_snapshot_preview1", "proc_raise"), policy)
	testutil.FailErr(t, "audit denied", err)
	if deniedReport.Passed || len(deniedReport.Denied) != 1 {
		t.Fatalf("expected denied failure, got %+v", deniedReport)
	}

	undeclaredReport, err := AuditModuleImports(ctx, wasmModuleWithImport("env", "host_escape"), policy)
	testutil.FailErr(t, "audit undeclared", err)
	if undeclaredReport.Passed || len(undeclaredReport.Undeclared) != 1 {
		t.Fatalf("expected undeclared failure, got %+v", undeclaredReport)
	}
}

func TestWazeroRuntimeFreshInstantiationAndSettle(t *testing.T) {
	ctx := context.Background()
	clk := NewFixedClock(time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC))
	rt, err := NewWazeroRuntime(ctx, RuntimeConfig{
		WasmBytes:          wasmFreshCounterModule(),
		EnforceImportAudit: true,
		Clock:              clk,
		Entropy:            NewDeterministicEntropy(42),
	})
	testutil.FailErr(t, "new wazero runtime", err)
	defer rt.Close(ctx)

	for i := 0; i < 2; i++ {
		ws := &stubWorkspaceAdapter{}
		res, err := rt.Execute(ctx, Invocation{
			ExecutionID: "fresh-check",
			Mode:        ModeCode,
			Code:        "pass",
			Workspace:   ws,
		})
		testutil.FailErr(t, "execute fresh counter", err)
		if !res.CleanCompletion || res.ExitCode != 0 {
			t.Fatalf("run %d: expected clean exit 0, got exit=%d clean=%v diag=%v", i, res.ExitCode, res.CleanCompletion, res.Diagnostics)
		}
		if !ws.settled || !ws.settledClean {
			t.Fatalf("run %d: expected WorkspaceAdapter.Settle(true), got settled=%v clean=%v", i, ws.settled, ws.settledClean)
		}
	}
}

func TestWazeroRuntimeMemoryLimit(t *testing.T) {
	ctx := context.Background()

	// Static memory section exceeding DefaultMemoryLimitPages is rejected at compile time.
	if _, err := NewWazeroRuntime(ctx, RuntimeConfig{
		WasmBytes:               wasmExceedMemoryModule(8),
		DefaultMemoryLimitPages: 4,
	}); err == nil || !strings.Contains(err.Error(), "over limit") {
		t.Fatalf("expected compile-time memory limit rejection, got %v", err)
	}

	// Runtime memory.grow beyond DefaultMemoryLimitPages fails and out-of-bounds access latches StopMemoryLimitExceeded.
	rt, err := NewWazeroRuntime(ctx, RuntimeConfig{
		WasmBytes:               wasmMemoryGrowAndOOBModule(),
		DefaultMemoryLimitPages: 4,
	})
	testutil.FailErr(t, "new wazero runtime with memory limit", err)
	defer rt.Close(ctx)

	ws := &stubWorkspaceAdapter{}
	res, err := rt.Execute(ctx, Invocation{
		ExecutionID: "oom-check",
		Mode:        ModeCode,
		Code:        "pass",
		Workspace:   ws,
	})
	testutil.FailErr(t, "execute over-memory module", err)
	if res.CleanCompletion {
		t.Fatal("expected CleanCompletion=false when memory limit is exceeded")
	}
	if res.Stop == nil || res.Stop.Code != StopMemoryLimitExceeded {
		t.Fatalf("expected StopMemoryLimitExceeded, got %+v (diag=%v)", res.Stop, res.Diagnostics)
	}
	if !ws.settled || ws.settledClean {
		t.Fatalf("expected WorkspaceAdapter.Settle(false), got settled=%v clean=%v", ws.settled, ws.settledClean)
	}
}

func TestWazeroRuntimeContextCancellation(t *testing.T) {
	ctx := context.Background()
	rt, err := NewWazeroRuntime(ctx, RuntimeConfig{
		WasmBytes: wasmInfiniteLoopModule(),
	})
	testutil.FailErr(t, "new wazero runtime for loop", err)
	defer rt.Close(ctx)

	ws := &stubWorkspaceAdapter{}
	res, err := rt.Execute(ctx, Invocation{
		ExecutionID: "cancel-check",
		Mode:        ModeCode,
		Code:        "while True: pass",
		Limits: ResourceLimits{
			Timeout: 50 * time.Millisecond,
		},
		Workspace: ws,
	})
	testutil.FailErr(t, "execute infinite loop", err)
	if res.CleanCompletion {
		t.Fatal("expected CleanCompletion=false on cancelled loop")
	}
	if res.Stop == nil || res.Stop.Code != StopCancelled {
		t.Fatalf("expected StopCancelled, got %+v", res.Stop)
	}
	if !ws.settled || ws.settledClean {
		t.Fatalf("expected WorkspaceAdapter.Settle(false), got settled=%v clean=%v", ws.settled, ws.settledClean)
	}
}

func TestBuildArgvAndClockEntropy(t *testing.T) {
	argv, err := BuildArgv(Invocation{Mode: ModeModule, Module: "unittest", Args: []string{"-v"}})
	testutil.FailErr(t, "build module argv", err)
	want := []string{"python3", "-I", "-B", "-m", "unittest", "-v"}
	if strings.Join(argv, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("argv = %v, want %v", argv, want)
	}

	if _, err := BuildArgv(Invocation{Mode: ModeCode}); err == nil {
		t.Fatal("expected error for empty code")
	}

	clk := NewFixedClock(time.Unix(1700000000, 0))
	clk.Advance(250 * time.Millisecond)
	if clk.MonotonicNanos() != int64(250*time.Millisecond) {
		t.Fatalf("monotonic = %d, want %d", clk.MonotonicNanos(), int64(250*time.Millisecond))
	}

	e1 := NewDeterministicEntropy(99)
	e2 := NewDeterministicEntropy(99)
	b1, b2 := make([]byte, 16), make([]byte, 16)
	_, err = e1.Reader().Read(b1)
	testutil.FailErr(t, "read entropy 1", err)
	_, err = e2.Reader().Read(b2)
	testutil.FailErr(t, "read entropy 2", err)
	if !bytes.Equal(b1, b2) {
		t.Fatal("deterministic entropy with identical seed diverged")
	}
}

// --- Test helpers & minimal valid Wasm binary builders ---

type stubWorkspaceAdapter struct {
	settled      bool
	settledClean bool
}

func (s *stubWorkspaceAdapter) Open(context.Context, OpenRequest) (GuestFile, Errno) {
	return nil, ErrnoENOENT
}
func (s *stubWorkspaceAdapter) Stat(context.Context, string) (StatInfo, Errno) {
	return StatInfo{}, ErrnoENOENT
}
func (s *stubWorkspaceAdapter) Lstat(context.Context, string) (StatInfo, Errno) {
	return StatInfo{}, ErrnoENOENT
}
func (s *stubWorkspaceAdapter) ReadDir(context.Context, string) ([]DirEntry, Errno) {
	return nil, ErrnoENOENT
}
func (s *stubWorkspaceAdapter) Mkdir(context.Context, string, fs.FileMode) Errno { return ErrnoEROFS }
func (s *stubWorkspaceAdapter) Unlink(context.Context, string) Errno             { return ErrnoEROFS }
func (s *stubWorkspaceAdapter) Rmdir(context.Context, string) Errno              { return ErrnoEROFS }
func (s *stubWorkspaceAdapter) Rename(context.Context, string, string) Errno     { return ErrnoEXDEV }
func (s *stubWorkspaceAdapter) Readlink(context.Context, string) (string, Errno) {
	return "", ErrnoEINVAL
}
func (s *stubWorkspaceAdapter) Settle(_ context.Context, clean bool) (*SettlementReport, error) {
	s.settled = true
	s.settledClean = clean
	return &SettlementReport{CleanCompletion: clean}, nil
}

func wasmSection(id byte, payload []byte) []byte {
	out := []byte{id}
	out = append(out, encodeULEB128(uint32(len(payload)))...)
	return append(out, payload...)
}

func encodeULEB128(v uint32) []byte {
	var out []byte
	for {
		b := byte(v & 0x7f)
		v >>= 7
		if v != 0 {
			b |= 0x80
		}
		out = append(out, b)
		if v == 0 {
			return out
		}
	}
}

func wasmString(s string) []byte {
	out := encodeULEB128(uint32(len(s)))
	return append(out, []byte(s)...)
}

func wasmModuleWithImport(modName, fnName string) []byte {
	mod := []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}
	// Type 0: () -> ()
	mod = append(mod, wasmSection(1, []byte{0x01, 0x60, 0x00, 0x00})...)
	// Import 0: modName.fnName func type 0
	imp := []byte{0x01}
	imp = append(imp, wasmString(modName)...)
	imp = append(imp, wasmString(fnName)...)
	imp = append(imp, 0x00, 0x00)
	mod = append(mod, wasmSection(2, imp)...)
	return mod
}

func wasmFreshCounterModule() []byte {
	mod := []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}
	// Type section: type 0 = (i32)->(), type 1 = ()->()
	mod = append(mod, wasmSection(1, []byte{
		0x02,
		0x60, 0x01, 0x7f, 0x00,
		0x60, 0x00, 0x00,
	})...)
	// Import section: wasi_snapshot_preview1.proc_exit (func idx 0, type 0)
	imp := []byte{0x01}
	imp = append(imp, wasmString("wasi_snapshot_preview1")...)
	imp = append(imp, wasmString("proc_exit")...)
	imp = append(imp, 0x00, 0x00)
	mod = append(mod, wasmSection(2, imp)...)
	// Function section: func idx 1 uses type 1
	mod = append(mod, wasmSection(3, []byte{0x01, 0x01})...)
	// Memory section: 1 memory, min 1 page
	mod = append(mod, wasmSection(5, []byte{0x01, 0x00, 0x01})...)
	// Global section: 1 mutable i32 = 0
	mod = append(mod, wasmSection(6, []byte{0x01, 0x7f, 0x01, 0x41, 0x00, 0x0b})...)
	// Export section: "memory" (mem 0), "_start" (func 1)
	exp := []byte{0x02}
	exp = append(exp, wasmString("memory")...)
	exp = append(exp, 0x02, 0x00)
	exp = append(exp, wasmString("_start")...)
	exp = append(exp, 0x00, 0x01)
	mod = append(mod, wasmSection(7, exp)...)
	// Code section: increment global 0, call proc_exit(global0 - 1)
	body := []byte{
		0x00,       // 0 locals
		0x23, 0x00, // global.get 0
		0x41, 0x01, // i32.const 1
		0x6a,       // i32.add
		0x24, 0x00, // global.set 0
		0x23, 0x00, // global.get 0
		0x41, 0x01, // i32.const 1
		0x6b,       // i32.sub
		0x10, 0x00, // call 0 (proc_exit)
		0x0b,       // end
	}
	code := []byte{0x01}
	code = append(code, encodeULEB128(uint32(len(body)))...)
	code = append(code, body...)
	mod = append(mod, wasmSection(10, code)...)
	return mod
}

func wasmInfiniteLoopModule() []byte {
	mod := []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}
	mod = append(mod, wasmSection(1, []byte{0x01, 0x60, 0x00, 0x00})...)
	mod = append(mod, wasmSection(3, []byte{0x01, 0x00})...)
	mod = append(mod, wasmSection(5, []byte{0x01, 0x00, 0x01})...)
	exp := []byte{0x02}
	exp = append(exp, wasmString("memory")...)
	exp = append(exp, 0x02, 0x00)
	exp = append(exp, wasmString("_start")...)
	exp = append(exp, 0x00, 0x00)
	mod = append(mod, wasmSection(7, exp)...)
	body := []byte{
		0x00,       // 0 locals
		0x03, 0x40, // loop (void)
		0x0c, 0x00, // br 0
		0x0b,       // end loop
		0x0b,       // end func
	}
	code := []byte{0x01}
	code = append(code, encodeULEB128(uint32(len(body)))...)
	code = append(code, body...)
	mod = append(mod, wasmSection(10, code)...)
	return mod
}

func wasmExceedMemoryModule(minPages byte) []byte {
	mod := []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}
	mod = append(mod, wasmSection(1, []byte{0x01, 0x60, 0x00, 0x00})...)
	mod = append(mod, wasmSection(3, []byte{0x01, 0x00})...)
	mod = append(mod, wasmSection(5, []byte{0x01, 0x00, minPages})...)
	exp := []byte{0x02}
	exp = append(exp, wasmString("memory")...)
	exp = append(exp, 0x02, 0x00)
	exp = append(exp, wasmString("_start")...)
	exp = append(exp, 0x00, 0x00)
	mod = append(mod, wasmSection(7, exp)...)
	body := []byte{0x00, 0x0b}
	code := []byte{0x01}
	code = append(code, encodeULEB128(uint32(len(body)))...)
	code = append(code, body...)
	mod = append(mod, wasmSection(10, code)...)
	return mod
}

func wasmMemoryGrowAndOOBModule() []byte {
	mod := []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}
	mod = append(mod, wasmSection(1, []byte{0x01, 0x60, 0x00, 0x00})...)
	mod = append(mod, wasmSection(3, []byte{0x01, 0x00})...)
	mod = append(mod, wasmSection(5, []byte{0x01, 0x00, 0x01})...)
	exp := []byte{0x02}
	exp = append(exp, wasmString("memory")...)
	exp = append(exp, 0x02, 0x00)
	exp = append(exp, wasmString("_start")...)
	exp = append(exp, 0x00, 0x00)
	mod = append(mod, wasmSection(7, exp)...)
	body := []byte{
		0x00,             // 0 locals
		0x41, 0x08,       // i32.const 8
		0x40, 0x00,       // memory.grow 0 (fails against 4-page limit -> returns -1)
		0x1a,             // drop
		0x41, 0x80, 0x80, 0x14, // i32.const 327680 (5 * 64 KiB)
		0x28, 0x02, 0x00, // i32.load -> traps out of bounds
		0x1a,             // drop
		0x0b,             // end
	}
	code := []byte{0x01}
	code = append(code, encodeULEB128(uint32(len(body)))...)
	code = append(code, body...)
	mod = append(mod, wasmSection(10, code)...)
	return mod
}
