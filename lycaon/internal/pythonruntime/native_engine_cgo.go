//go:build cgo && (darwin || linux)

package pythonruntime

/*
#cgo linux LDFLAGS: -ldl
#include <dlfcn.h>
#include <stdlib.h>
#include "pw_py_engine.h"

typedef const pw_py_engine_identity_t* (*fn_pw_py_engine_identity)(void);
typedef pw_py_status_t (*fn_pw_py_engine_init)(void);
typedef pw_py_status_t (*fn_pw_py_instance_new)(const pw_py_instance_config_t*, pw_py_host_wasi_fn, void*, pw_py_instance_t**);
typedef pw_py_status_t (*fn_pw_py_instance_run)(pw_py_instance_t*, pw_py_run_result_t*);
typedef void (*fn_pw_py_instance_cancel)(pw_py_instance_t*);
typedef pw_py_status_t (*fn_pw_py_instance_memory)(pw_py_instance_t*, uint8_t**, uint64_t*, uint32_t*, uint32_t*);
typedef void (*fn_pw_py_instance_free)(pw_py_instance_t*);

typedef struct pw_py_lib {
    void* handle;
    fn_pw_py_engine_identity engine_identity;
    fn_pw_py_engine_init engine_init;
    fn_pw_py_instance_new instance_new;
    fn_pw_py_instance_run instance_run;
    fn_pw_py_instance_cancel instance_cancel;
    fn_pw_py_instance_memory instance_memory;
    fn_pw_py_instance_free instance_free;
} pw_py_lib_t;

static pw_py_lib_t* pw_py_lib_open(const char* path, const char** err_out) {
    void* h = dlopen(path, RTLD_LOCAL | RTLD_NOW);
    if (!h) {
        *err_out = dlerror();
        return NULL;
    }
    pw_py_lib_t* out = (pw_py_lib_t*)calloc(1, sizeof(pw_py_lib_t));
    if (!out) {
        *err_out = "out of memory allocating pw_py_lib_t";
        dlclose(h);
        return NULL;
    }
    out->handle = h;
    out->engine_identity = (fn_pw_py_engine_identity)dlsym(h, "pw_py_engine_identity");
    out->engine_init     = (fn_pw_py_engine_init)dlsym(h, "pw_py_engine_init");
    out->instance_new    = (fn_pw_py_instance_new)dlsym(h, "pw_py_instance_new");
    out->instance_run    = (fn_pw_py_instance_run)dlsym(h, "pw_py_instance_run");
    out->instance_cancel = (fn_pw_py_instance_cancel)dlsym(h, "pw_py_instance_cancel");
    out->instance_memory = (fn_pw_py_instance_memory)dlsym(h, "pw_py_instance_memory");
    out->instance_free   = (fn_pw_py_instance_free)dlsym(h, "pw_py_instance_free");
    if (!out->engine_identity || !out->engine_init || !out->instance_new ||
        !out->instance_run || !out->instance_cancel || !out->instance_memory ||
        !out->instance_free) {
        *err_out = "missing one or more required pw_py_* exported symbols";
        dlclose(h);
        free(out);
        return NULL;
    }
    return out;
}

static void pw_py_lib_close(pw_py_lib_t* lib) {
    if (lib) {
        if (lib->handle) {
            dlclose(lib->handle);
            lib->handle = NULL;
        }
        free(lib);
    }
}

static const pw_py_engine_identity_t* pw_py_call_identity(pw_py_lib_t* lib) {
    return lib->engine_identity();
}

static pw_py_status_t pw_py_call_init(pw_py_lib_t* lib) {
    return lib->engine_init();
}

extern uint32_t pwGoWasiCallback(
    uintptr_t handle_val,
    uint32_t op_id,
    uint64_t* args,
    uint32_t argc,
    uint32_t* out_ret);

static pw_py_wasi_action_t pw_py_c_wasi_trampoline(
    void* host_ctx,
    pw_py_wasi_op_t op_id,
    const char* op_name,
    const uint64_t* args,
    uint32_t argc,
    uint32_t* out_ret) {
    (void)op_name;
    return (pw_py_wasi_action_t)pwGoWasiCallback((uintptr_t)host_ctx, (uint32_t)op_id, (uint64_t*)args, argc, out_ret);
}

static pw_py_status_t pw_py_call_instance_new(
    pw_py_lib_t* lib,
    const pw_py_instance_config_t* cfg,
    uintptr_t handle_val,
    pw_py_instance_t** out_inst) {
    return lib->instance_new(cfg, pw_py_c_wasi_trampoline, (void*)handle_val, out_inst);
}

static pw_py_status_t pw_py_call_instance_run(
    pw_py_lib_t* lib,
    pw_py_instance_t* inst,
    pw_py_run_result_t* out_res) {
    return lib->instance_run(inst, out_res);
}

static void pw_py_call_instance_cancel(pw_py_lib_t* lib, pw_py_instance_t* inst) {
    lib->instance_cancel(inst);
}

static pw_py_status_t pw_py_call_instance_memory(
    pw_py_lib_t* lib,
    pw_py_instance_t* inst,
    uint8_t** out_data,
    uint64_t* out_size,
    uint32_t* out_pages,
    uint32_t* out_max_pages) {
    return lib->instance_memory(inst, out_data, out_size, out_pages, out_max_pages);
}

static void pw_py_call_instance_free(pw_py_lib_t* lib, pw_py_instance_t* inst) {
    lib->instance_free(inst);
}
*/
import "C"

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"runtime"
	"runtime/cgo"
	"sort"
	"strings"
	"sync"
	"unsafe"
)

type nativeExecHandle struct {
	lib  *C.pw_py_lib_t
	inst *C.pw_py_instance_t
	host *nativeWASIHost
}

//export pwGoWasiCallback
func pwGoWasiCallback(handleVal C.uintptr_t, opID C.uint32_t, argsPtr *C.uint64_t, argc C.uint32_t, outRet *C.uint32_t) C.uint32_t {
	h := cgo.Handle(handleVal)
	ctxObj, ok := h.Value().(*nativeExecHandle)
	if !ok || ctxObj == nil || ctxObj.host == nil {
		if outRet != nil {
			*outRet = C.uint32_t(wasiErrnoIO)
		}
		return C.uint32_t(C.PW_PY_WASI_TRAP)
	}

	var mem []byte
	var dataPtr *C.uint8_t
	var memSize C.uint64_t
	var pages, maxPages C.uint32_t
	if ctxObj.inst != nil && C.pw_py_call_instance_memory(ctxObj.lib, ctxObj.inst, &dataPtr, &memSize, &pages, &maxPages) == C.PW_PY_STATUS_OK && dataPtr != nil && memSize > 0 {
		mem = unsafe.Slice((*byte)(unsafe.Pointer(dataPtr)), int(memSize))
	}

	var args []uint64
	if argc > 0 && argsPtr != nil {
		args = unsafe.Slice((*uint64)(unsafe.Pointer(argsPtr)), int(argc))
	}

	action, ret := ctxObj.host.dispatch(mem, uint32(opID), args)
	if outRet != nil {
		*outRet = C.uint32_t(ret)
	}
	return C.uint32_t(action)
}

// NativeEngineRuntime executes isolated Portable Python invocations using
// PY07's ahead-of-time compiled native CPython/WASI shared library loaded via
// dlopen after complete fail-closed manifest, SHA-256, ABI, and image verification.
type NativeEngineRuntime struct {
	mu                  sync.Mutex
	closed              bool
	lib                 *C.pw_py_lib_t
	identity            NativeEngineIdentity
	manifest            *ImageManifest
	imageDir            string
	scratchRoot         string
	workspaceMount      string
	useTrustedBootstrap bool
	audit               *ImportAuditReport
	clock               Clock
	entropy             Entropy
	parked              *ParkedGuestTracker
	memLimitPgs         uint32
}

// NewNativeEngineRuntime verifies the staged native engine artifact, opens it
// via dlopen, performs the fail-closed pw_py_engine_identity() + image pairing
// handshake, and initializes the engine. It NEVER silently falls back to wazero.
func NewNativeEngineRuntime(_ context.Context, cfg RuntimeConfig) (Runtime, error) {
	bundled := cfg.EngineManifest
	if bundled == nil {
		var err error
		if strings.TrimSpace(cfg.EngineManifestPath) != "" {
			bundled, err = LoadBundledEngineManifestFile(cfg.EngineManifestPath)
		} else {
			bundled, err = LoadBundledEngineManifest()
		}
		if err != nil {
			return nil, &NativeEngineVerificationError{
				Stage:   "manifest",
				Message: "failed to load bundled portable python native engine manifest",
				Err:     err,
			}
		}
	}
	if err := ValidateBundledEngineManifest(bundled); err != nil {
		return nil, &NativeEngineVerificationError{
			Stage:   "manifest",
			Message: "bundled portable python native engine manifest is invalid or unpinned",
			Err:     err,
		}
	}

	engineDir := strings.TrimSpace(cfg.EngineDir)
	if engineDir == "" {
		var err error
		engineDir, err = DiscoverEngineDir("")
		if err != nil {
			return nil, err
		}
	}

	art, err := bundled.ArtifactForCurrentPlatform()
	if err != nil {
		return nil, &NativeEngineVerificationError{
			Stage:   "platform",
			Message: fmt.Sprintf("no pinned native python engine artifact for %s/%s", runtime.GOOS, runtime.GOARCH),
			Err:     err,
		}
	}
	libPath, err := VerifyEngineDir(bundled, engineDir, runtime.GOOS, runtime.GOARCH, art)
	if err != nil {
		return nil, &NativeEngineVerificationError{
			Stage:   "artifact",
			Message: "staged native python engine failed SHA-256/manifest verification before dlopen",
			Err:     err,
		}
	}

	cPath := C.CString(libPath)
	defer C.free(unsafe.Pointer(cPath))
	var cErr *C.char
	lib := C.pw_py_lib_open(cPath, &cErr)
	if lib == nil {
		msg := "dlopen failed"
		if cErr != nil {
			msg = C.GoString(cErr)
		}
		return nil, &NativeEngineVerificationError{
			Stage:   "dlopen",
			Message: fmt.Sprintf("failed to load verified native engine %s: %s", libPath, msg),
		}
	}

	cID := C.pw_py_call_identity(lib)
	if cID == nil {
		C.pw_py_lib_close(lib)
		return nil, &NativeEngineVerificationError{
			Stage:   "abi",
			Message: "pw_py_engine_identity() returned NULL",
		}
	}
	reported := cIdentityToGo(cID)

	verifiedID, err := VerifyNativeEngineHandshake(HandshakeVerificationInput{
		BundledManifest:  bundled,
		EngineDir:        engineDir,
		ImageDir:         cfg.ImageDir,
		ImageManifest:    cfg.Manifest,
		ReportedIdentity: reported,
		GOOS:             runtime.GOOS,
		GOARCH:           runtime.GOARCH,
	})
	if err != nil {
		C.pw_py_lib_close(lib)
		return nil, err
	}

	if st := C.pw_py_call_init(lib); st != C.PW_PY_STATUS_OK {
		C.pw_py_lib_close(lib)
		return nil, &NativeEngineVerificationError{
			Stage:   "init",
			Message: fmt.Sprintf("pw_py_engine_init() failed with status %d", int(st)),
		}
	}

	clk := cfg.Clock
	if clk == nil {
		clk = NewSystemClock()
	}
	ent := cfg.Entropy
	if ent == nil {
		ent = CryptoEntropy{}
	}
	wsMount := cfg.WorkspaceMount
	if strings.TrimSpace(wsMount) == "" {
		wsMount = DefaultGuestWorkspaceMount
	}
	if strings.TrimSpace(cfg.ScratchRoot) != "" {
		if err := os.MkdirAll(cfg.ScratchRoot, 0o700); err != nil {
			C.pw_py_lib_close(lib)
			return nil, fmt.Errorf("create scratch root: %w", err)
		}
		_, _ = CleanOrphanedScratchDirs(cfg.ScratchRoot)
	}

	policy := DefaultWASIImportPolicy()
	return &NativeEngineRuntime{
		lib:                 lib,
		identity:            *verifiedID,
		manifest:            cfg.Manifest,
		imageDir:            cfg.ImageDir,
		scratchRoot:         cfg.ScratchRoot,
		workspaceMount:      wsMount,
		useTrustedBootstrap: cfg.UseTrustedBootstrap,
		audit:               &ImportAuditReport{Observed: policy, Passed: true},
		clock:               clk,
		entropy:             ent,
		parked:              NewParkedGuestTracker(cfg.ParkedBudget),
		memLimitPgs:         cfg.DefaultMemoryLimitPages,
	}, nil
}

func cIdentityToGo(c *C.pw_py_engine_identity_t) *NativeEngineIdentity {
	if c == nil {
		return nil
	}
	goStr := func(p *C.char) string {
		if p == nil {
			return ""
		}
		return C.GoString(p)
	}
	return &NativeEngineIdentity{
		ABIVersion:            uint32(c.abi_version),
		ImportCount:           uint32(c.import_count),
		EngineTag:             goStr(c.engine_tag),
		SourceCommit:          goStr(c.source_commit),
		Platform:              goStr(c.platform),
		CPythonVersion:        goStr(c.cpython_version),
		CPythonWasmSHA256:     goStr(c.cpython_wasm_sha256),
		MeteredWasmSHA256:     goStr(c.metered_wasm_sha256),
		WABTVersion:           goStr(c.wabt_version),
		FuelAlgorithmVersion:  goStr(c.fuel_algorithm_version),
		ImportInventorySHA256: goStr(c.import_inventory_sha256),
	}
}

func (r *NativeEngineRuntime) Manifest() *ImageManifest          { return r.manifest }
func (r *NativeEngineRuntime) ImportAudit() *ImportAuditReport   { return r.audit }
func (r *NativeEngineRuntime) ParkedGuests() *ParkedGuestTracker { return r.parked }

func (r *NativeEngineRuntime) Info() RuntimeInfo {
	id := r.identity
	return RuntimeInfo{
		Backend:      BackendNativeAOT,
		NativeEngine: &id,
	}
}

func (r *NativeEngineRuntime) Close(_ context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	r.parked.ReleaseAll()
	if r.lib != nil {
		C.pw_py_lib_close(r.lib)
		r.lib = nil
	}
	return nil
}

func (r *NativeEngineRuntime) Execute(ctx context.Context, inv Invocation) (*ExecutionResult, error) {
	start := r.clock.Now()
	execCtx, cancel := context.WithCancel(ctx)
	if inv.Limits.Timeout > 0 {
		execCtx, cancel = context.WithTimeout(ctx, inv.Limits.Timeout)
	}
	defer cancel()

	latch := NewStopLatch(cancel)
	latch.now = r.clock.Now
	if inv.StopLatch != nil {
		inv.StopLatch.RegisterSealer(func() {
			if stop := inv.StopLatch.Stop(); stop != nil {
				latch.Trigger(*stop)
			}
		})
		latch.RegisterSealer(func() {
			if stop := latch.Stop(); stop != nil {
				inv.StopLatch.Trigger(*stop)
			}
		})
	}
	meter := NewWorkMeter(inv.Limits.MaxGuestWork, inv.Limits.MaxHostWork, inv.Limits.MaxStagedBytes, latch)

	var (
		argv []string
		err  error
	)
	if r.useTrustedBootstrap {
		cwd := normalizeGuestCWD(r.workspaceMount, inv.CWD)
		argv, err = BuildBootstrapArgv(BootstrapConfig{
			Mode:           inv.Mode,
			Code:           inv.Code,
			Script:         inv.Script,
			Module:         inv.Module,
			CLI:            inv.CLI,
			CWD:            cwd,
			Args:           inv.Args,
			PackagesMount:  DefaultGuestPackagesMount,
			ScratchMount:   DefaultGuestScratchMount,
			HTTPBridgePath: inv.HTTPBridgePath,
		})
	} else {
		argv, err = BuildArgv(inv)
	}
	if err != nil {
		return nil, err
	}

	invScratch, err := os.MkdirTemp(r.scratchRoot, "pw-py-scratch-*")
	if err != nil {
		return nil, fmt.Errorf("create per-invocation scratch dir: %w", err)
	}
	defer os.RemoveAll(invScratch)

	var stderrCapture bytes.Buffer
	stderrTarget := io.Writer(&stderrCapture)
	if inv.Stderr != nil {
		stderrTarget = io.MultiWriter(inv.Stderr, &stderrCapture)
	}
	sealedStdout := NewSealedWriter(inv.Stdout, latch, inv.Limits.MaxOutputBytes)
	sealedStderr := NewSealedWriter(stderrTarget, latch, inv.Limits.MaxOutputBytes)

	preopens, wsFS := r.buildPreopens(execCtx, inv, latch, meter, invScratch)
	wasiHost := newNativeWASIHost(
		execCtx,
		argv,
		inv.Env,
		inv.Stdin,
		sealedStdout,
		sealedStderr,
		r.clock,
		r.entropy,
		latch,
		meter,
		preopens,
	)
	defer wasiHost.closeAll()

	if inv.ExecutionID != "" {
		defer r.parked.Release(inv.ExecutionID)
	}

	maxPages := inv.Limits.MaxMemoryPages
	if maxPages == 0 {
		maxPages = r.memLimitPgs
	}

	// Lock the calling goroutine to one OS thread for single-thread affinity
	// across pw_py_instance_new -> pw_py_instance_run -> pw_py_instance_free.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	execHandle := &nativeExecHandle{
		lib:  r.lib,
		host: wasiHost,
	}
	cgoHandle := cgo.NewHandle(execHandle)
	defer cgoHandle.Delete()

	instCfg := C.pw_py_instance_config_t{
		struct_size:      C.uint32_t(unsafe.Sizeof(C.pw_py_instance_config_t{})),
		max_memory_pages: C.uint32_t(maxPages),
		fuel_budget:      C.uint64_t(inv.Limits.MaxGuestWork),
	}
	var inst *C.pw_py_instance_t
	if st := C.pw_py_call_instance_new(r.lib, &instCfg, C.uintptr_t(cgoHandle), &inst); st != C.PW_PY_STATUS_OK {
		return nil, fmt.Errorf("pw_py_instance_new failed with status %d", int(st))
	}
	execHandle.inst = inst

	var freeOnce sync.Once
	freeInstance := func() {
		freeOnce.Do(func() {
			if inst != nil {
				C.pw_py_call_instance_free(r.lib, inst)
				inst = nil
				execHandle.inst = nil
			}
		})
	}
	defer freeInstance()

	// Asynchronous cancellation watcher: interrupts pure CPU loops via
	// pw_py_instance_cancel(inst) when execCtx is canceled or times out.
	// Deferred stop runs BEFORE freeInstance() so cancel is never called on a freed instance.
	doneCh := make(chan struct{})
	watcherDone := make(chan struct{})
	var stopWatcherOnce sync.Once
	stopWatcher := func() {
		stopWatcherOnce.Do(func() {
			close(doneCh)
			<-watcherDone
		})
	}
	cancelTarget := inst
	go func() {
		defer close(watcherDone)
		select {
		case <-execCtx.Done():
			C.pw_py_call_instance_cancel(r.lib, cancelTarget)
		case <-doneCh:
		}
	}()
	defer stopWatcher()

	var runRes C.pw_py_run_result_t
	_ = C.pw_py_call_instance_run(r.lib, inst, &runRes)
	stopWatcher()

	// Capture open write descriptor count BEFORE closing host FDs.
	var unclosedWriteDescs int64
	if wsFS != nil {
		unclosedWriteDescs = wsFS.OpenWriteDescriptors()
	}

	// Deterministic exactly-once guest teardown after run returns.
	freeInstance()
	wasiHost.closeAll()

	meter.SetGuestWork(uint64(runRes.guest_work_used))
	res := &ExecutionResult{
		ExecutionID: inv.ExecutionID,
		Backend:     BackendNativeAOT,
		EngineTag:   r.identity.EngineTag,
		GuestStderr: stderrCapture.String(),
		Usage: ResourceUsage{
			GuestWork:       meter.GuestWork(),
			HostWork:        meter.HostWork(),
			PeakMemoryPages: uint32(runRes.peak_memory_pages),
			StdoutBytes:     sealedStdout.WrittenBytes(),
			StderrBytes:     sealedStderr.WrittenBytes(),
			StagedBytes:     meter.StagedBytes(),
			Duration:        r.clock.Now().Sub(start),
		},
	}

	r.classifyNativeOutcome(execCtx, latch, runRes, inv.Limits.MaxGuestWork, res)

	if r.useTrustedBootstrap && wasiHost.procExit {
		res.CleanCompletion = false
		res.Diagnostics = append(res.Diagnostics, fmt.Sprintf("abrupt guest exit via proc_exit(%d) bypassed clean finalization", wasiHost.exitCode))
	}
	if sealedStdout.HadError() || sealedStderr.HadError() {
		res.CleanCompletion = false
		res.Diagnostics = append(res.Diagnostics, "guest output stream reported write or seal error")
	}
	if unclosedWriteDescs > 0 {
		res.CleanCompletion = false
		res.Diagnostics = append(res.Diagnostics, fmt.Sprintf("guest exited with %d unclosed workspace write descriptor(s)", unclosedWriteDescs))
	}

	if inv.Workspace != nil {
		report, settleErr := inv.Workspace.Settle(context.Background(), res.CleanCompletion)
		res.Settlement = report
		if settleErr != nil {
			res.CleanCompletion = false
			res.Diagnostics = append(res.Diagnostics, fmt.Sprintf("workspace settlement failed: %v", settleErr))
		}
		if report != nil {
			if len(report.Conflicts) > 0 {
				res.CleanCompletion = false
				latch.Trigger(TerminalStop{
					Code:   StopRevisionConflict,
					Reason: fmt.Sprintf("workspace settlement detected %d revision conflict(s)", len(report.Conflicts)),
				})
				res.Stop = latch.Stop()
				res.Diagnostics = append(res.Diagnostics, fmt.Sprintf("workspace settlement revision conflicts: %d", len(report.Conflicts)))
			}
			if report.PartialFailure != nil {
				res.CleanCompletion = false
				res.Diagnostics = append(res.Diagnostics, fmt.Sprintf("workspace settlement partial failure: %v", report.PartialFailure))
			}
		}
	}

	return res, nil
}

func (r *NativeEngineRuntime) classifyNativeOutcome(
	execCtx context.Context,
	latch *StopLatch,
	runRes C.pw_py_run_result_t,
	fuelBudget uint64,
	res *ExecutionResult,
) {
	if stop := latch.Stop(); stop != nil {
		res.Stop = stop
		res.ExitCode = 1
		res.CleanCompletion = false
		return
	}
	switch runRes.status {
	case C.PW_PY_STATUS_OK:
		res.ExitCode = 0
		res.CleanCompletion = true
	case C.PW_PY_STATUS_PROC_EXIT:
		res.ExitCode = int(runRes.exit_code)
		res.CleanCompletion = runRes.exit_code == 0
	case C.PW_PY_STATUS_FUEL_EXHAUSTED:
		latch.Trigger(TerminalStop{
			Code:   StopWorkLimitExceeded,
			Reason: fmt.Sprintf("guest work fuel exhausted (limit %d)", fuelBudget),
		})
		res.Stop = latch.Stop()
		res.ExitCode = 1
		res.CleanCompletion = false
	case C.PW_PY_STATUS_CANCELED:
		reason := "execution canceled"
		if execCtx.Err() == context.DeadlineExceeded {
			reason = "execution timed out"
		}
		latch.Trigger(TerminalStop{Code: StopCancelled, Reason: reason})
		res.Stop = latch.Stop()
		res.ExitCode = 1
		res.CleanCompletion = false
	case C.PW_PY_STATUS_MEMORY_LIMIT:
		latch.Trigger(TerminalStop{
			Code:   StopMemoryLimitExceeded,
			Reason: "guest linear memory limit exceeded",
		})
		res.Stop = latch.Stop()
		res.ExitCode = 1
		res.CleanCompletion = false
	case C.PW_PY_STATUS_HOST_STOP:
		latch.Trigger(TerminalStop{
			Code:   StopEffectNotAllowed,
			Reason: "host stopped guest execution",
		})
		res.Stop = latch.Stop()
		res.ExitCode = 1
		res.CleanCompletion = false
	default:
		res.ExitCode = 1
		res.CleanCompletion = false
		res.Diagnostics = append(res.Diagnostics, fmt.Sprintf("native engine trap status=%d trap_code=%d", int(runRes.status), uint32(runRes.trap_code)))
	}
}

func (r *NativeEngineRuntime) buildPreopens(
	execCtx context.Context,
	inv Invocation,
	latch *StopLatch,
	meter *WorkMeter,
	invScratch string,
) ([]nativePreopen, *WorkspaceSysFS) {
	var out []nativePreopen
	out = append(out, nativePreopen{guestPath: "/dev", fs: NullDeviceFS{}})
	if r.imageDir != "" && r.manifest != nil {
		if stdlibAbs, err := resolveInsideDir(r.imageDir, r.manifest.Interpreter.StdlibPath); err == nil {
			out = append(out, nativePreopen{guestPath: "/usr/local/lib", fs: newHostDirSysFS(stdlibAbs, true)})
		}
		if r.manifest.PackagesRoot != "" {
			if pkgAbs, err := resolveInsideDir(r.imageDir, r.manifest.PackagesRoot); err == nil {
				out = append(out, nativePreopen{guestPath: DefaultGuestPackagesMount, fs: newHostDirSysFS(pkgAbs, true)})
			}
		}
	}
	if invScratch != "" {
		out = append(out, nativePreopen{guestPath: DefaultGuestScratchMount, fs: newHostDirSysFS(invScratch, false)})
	}
	var wsFS *WorkspaceSysFS
	if inv.Workspace != nil {
		wsFS = NewWorkspaceSysFS(WorkspaceSysFSConfig{
			Context:     execCtx,
			ExecutionID: inv.ExecutionID,
			Namespace:   NamespaceWorkspace,
			Adapter:     inv.Workspace,
			Observer:    inv.Observer,
			Latch:       latch,
			Meter:       meter,
		})
		out = append(out, nativePreopen{guestPath: r.workspaceMount, fs: wsFS})
	}
	if len(inv.ExtraSysMounts) > 0 {
		mounts := make([]string, 0, len(inv.ExtraSysMounts))
		for k := range inv.ExtraSysMounts {
			mounts = append(mounts, k)
		}
		sort.Strings(mounts)
		for _, guestMount := range mounts {
			if fsMount := inv.ExtraSysMounts[guestMount]; fsMount != nil {
				out = append(out, nativePreopen{guestPath: guestMount, fs: fsMount})
			}
		}
	}
	return out, wsFS
}
