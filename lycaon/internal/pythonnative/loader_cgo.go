//go:build cgo && ((darwin && arm64) || (linux && amd64))

package pythonnative

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

extern uint32_t pwNativeGoWasiCallback(
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
    return (pw_py_wasi_action_t)pwNativeGoWasiCallback((uintptr_t)host_ctx, (uint32_t)op_id, (uint64_t*)args, argc, out_ret);
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
	"fmt"
	"runtime/cgo"
	"sync"
	"unsafe"
)

// NativeAOTSupported reports true on cgo builds for darwin/arm64 and linux/amd64.
func NativeAOTSupported() bool { return true }

// Library owns a dlopen handle to a verified libpw_cpython_wasi shared library.
type Library struct {
	mu     sync.Mutex
	closed bool
	lib    *C.pw_py_lib_t
}

// Instance owns one isolated CPython/WASI guest instance.
type Instance struct {
	lib       *C.pw_py_lib_t
	inst      *C.pw_py_instance_t
	cgoHandle cgo.Handle
	freeOnce  sync.Once
}

type callbackState struct {
	lib      *C.pw_py_lib_t
	inst     *C.pw_py_instance_t
	dispatch WasiDispatchFunc
}

//export pwNativeGoWasiCallback
func pwNativeGoWasiCallback(handleVal C.uintptr_t, opID C.uint32_t, argsPtr *C.uint64_t, argc C.uint32_t, outRet *C.uint32_t) C.uint32_t {
	h := cgo.Handle(handleVal)
	st, ok := h.Value().(*callbackState)
	if !ok || st == nil || st.dispatch == nil {
		if outRet != nil {
			*outRet = 29 // WASI EIO
		}
		return C.uint32_t(C.PW_PY_WASI_TRAP)
	}
	var mem []byte
	var dataPtr *C.uint8_t
	var memSize C.uint64_t
	var pages, maxPages C.uint32_t
	if st.inst != nil && C.pw_py_call_instance_memory(st.lib, st.inst, &dataPtr, &memSize, &pages, &maxPages) == C.PW_PY_STATUS_OK && dataPtr != nil && memSize > 0 {
		mem = unsafe.Slice((*byte)(unsafe.Pointer(dataPtr)), int(memSize))
	}
	var args []uint64
	if argc > 0 && argsPtr != nil {
		args = unsafe.Slice((*uint64)(unsafe.Pointer(argsPtr)), int(argc))
	}
	action, ret := st.dispatch(mem, uint32(opID), args)
	if outRet != nil {
		*outRet = C.uint32_t(ret)
	}
	return C.uint32_t(action)
}

// Open loads libPath via dlopen(RTLD_LOCAL|RTLD_NOW) and resolves the 7 pw_py_* symbols.
func Open(libPath string) (*Library, error) {
	cPath := C.CString(libPath)
	defer C.free(unsafe.Pointer(cPath))
	var cErr *C.char
	lib := C.pw_py_lib_open(cPath, &cErr)
	if lib == nil {
		msg := "dlopen failed"
		if cErr != nil {
			msg = C.GoString(cErr)
		}
		return nil, fmt.Errorf("dlopen %s: %s", libPath, msg)
	}
	return &Library{lib: lib}, nil
}

// Identity queries pw_py_engine_identity().
func (l *Library) Identity() *Identity {
	if l == nil || l.lib == nil {
		return nil
	}
	c := C.pw_py_call_identity(l.lib)
	if c == nil {
		return nil
	}
	goStr := func(p *C.char) string {
		if p == nil {
			return ""
		}
		return C.GoString(p)
	}
	return &Identity{
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

// Init invokes pw_py_engine_init().
func (l *Library) Init() Status {
	if l == nil || l.lib == nil {
		return StatusInvalidArg
	}
	return Status(C.pw_py_call_init(l.lib))
}

// NewInstance allocates a fresh isolated guest instance wired to dispatch.
func (l *Library) NewInstance(cfg InstanceConfig, dispatch WasiDispatchFunc) (*Instance, Status) {
	if l == nil || l.lib == nil {
		return nil, StatusInvalidArg
	}
	st := &callbackState{
		lib:      l.lib,
		dispatch: dispatch,
	}
	h := cgo.NewHandle(st)
	instCfg := C.pw_py_instance_config_t{
		struct_size:      C.uint32_t(unsafe.Sizeof(C.pw_py_instance_config_t{})),
		max_memory_pages: C.uint32_t(cfg.MaxMemoryPages),
		fuel_budget:      C.uint64_t(cfg.FuelBudget),
	}
	var inst *C.pw_py_instance_t
	rc := Status(C.pw_py_call_instance_new(l.lib, &instCfg, C.uintptr_t(h), &inst))
	if rc != StatusOK {
		h.Delete()
		return nil, rc
	}
	st.inst = inst
	return &Instance{
		lib:       l.lib,
		inst:      inst,
		cgoHandle: h,
	}, StatusOK
}

// Close releases the dlopen handle. Must only be called after all active instances finish.
func (l *Library) Close() {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return
	}
	l.closed = true
	if l.lib != nil {
		C.pw_py_lib_close(l.lib)
		l.lib = nil
	}
}

// Run executes _start on the calling OS thread.
func (i *Instance) Run() RunResult {
	var res C.pw_py_run_result_t
	_ = C.pw_py_call_instance_run(i.lib, i.inst, &res)
	return RunResult{
		Status:          Status(res.status),
		ExitCode:        uint32(res.exit_code),
		TrapCode:        uint32(res.trap_code),
		PeakMemoryPages: uint32(res.peak_memory_pages),
		FuelRemaining:   int64(res.fuel_remaining),
		GuestWorkUsed:   uint64(res.guest_work_used),
	}
}

// Cancel asynchronously signals the running guest instance to unwind.
func (i *Instance) Cancel() {
	if i != nil && i.lib != nil && i.inst != nil {
		C.pw_py_call_instance_cancel(i.lib, i.inst)
	}
}

// Free deterministically releases the guest instance and its cgo handle exactly once.
func (i *Instance) Free() {
	if i == nil {
		return
	}
	i.freeOnce.Do(func() {
		if i.inst != nil {
			C.pw_py_call_instance_free(i.lib, i.inst)
			i.inst = nil
		}
		if i.cgoHandle != 0 {
			i.cgoHandle.Delete()
			i.cgoHandle = 0
		}
	})
}
