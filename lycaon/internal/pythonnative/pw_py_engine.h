/*
 * Painted Wolf Portable Python — Native CPython/WASI Engine C ABI
 *
 * Ahead-of-time compiled CPython 3.12.0/WASI (fuel-metered via
 * InstrumentWasmFuel + WABT wasm2c) packaged as a signed/authenticated
 * shared library (libpw_cpython_wasi.dylib / libpw_cpython_wasi.so).
 *
 * Exported symbol contract: ONLY symbols prefixed with `pw_py_` are exported.
 *
 * Lifecycle & Thread-Affinity Contract:
 *  - Call `pw_py_engine_init()` once (idempotent, thread-safe via pthread_once).
 *  - Each `pw_py_instance_t` owns fresh Wasm linear memory, globals, tables,
 *    and fuel state. Instances are completely isolated from one another and may
 *    run concurrently on distinct OS threads.
 *  - `pw_py_instance_run()` MUST run on a single OS thread at a time (enforced
 *    via atomic active-thread affinity check; concurrent `run` on the same
 *    instance returns `PW_PY_STATUS_THREAD_AFFINITY`).
 *  - `pw_py_instance_cancel()` is safe to call asynchronously from ANY thread
 *    while `pw_py_instance_run()` is executing a pure CPU-only loop.
 *  - `pw_py_instance_free()` MUST be called after `pw_py_instance_run()` has
 *    returned (never concurrently with `run`). Passing NULL is a no-op.
 *  - Inside `wasi_cb`, the host may inspect/mutate guest linear memory via
 *    `pw_py_instance_memory()`. Checked offset/length validation is the host's
 *    responsibility using the returned `out_size`.
 */

#ifndef PW_PY_ENGINE_H
#define PW_PY_ENGINE_H

#include <stddef.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

#if defined(_WIN32)
#define PW_PY_API __declspec(dllexport)
#elif defined(__GNUC__) || defined(__clang__)
#define PW_PY_API __attribute__((visibility("default")))
#else
#define PW_PY_API
#endif

#define PW_PY_ENGINE_ABI_VERSION 1U
#define PW_PY_ENGINE_IMPORT_COUNT 43U
#define PW_PY_WASM_PAGE_SIZE 65536U

/* Execution and API status codes. */
typedef enum pw_py_status {
    PW_PY_STATUS_OK = 0,
    PW_PY_STATUS_PROC_EXIT = 1,
    PW_PY_STATUS_FUEL_EXHAUSTED = 2,
    PW_PY_STATUS_CANCELED = 3,
    PW_PY_STATUS_MEMORY_LIMIT = 4,
    PW_PY_STATUS_HOST_STOP = 5,
    PW_PY_STATUS_TRAP = 6,
    PW_PY_STATUS_INVALID_ARG = 7,
    PW_PY_STATUS_THREAD_AFFINITY = 8,
    PW_PY_STATUS_ALLOC_FAILED = 9
} pw_py_status_t;

/* Control action returned by the host WASI callback. */
typedef enum pw_py_wasi_action {
    /* Continue guest execution; *out_ret carries the WASI errno (u32). */
    PW_PY_WASI_CONTINUE = 0,
    /* Guest called proc_exit; *out_ret carries the exit code (u32). */
    PW_PY_WASI_EXIT = 1,
    /* Host latched a terminal stop; unwind guest immediately. */
    PW_PY_WASI_HOST_STOP = 2,
    /* Host requests an immediate Wasm trap. */
    PW_PY_WASI_TRAP = 3
} pw_py_wasi_action_t;

/* Canonical sorted IDs for all 43 wasi_snapshot_preview1 imports. */
typedef enum pw_py_wasi_op {
    PW_PY_WASI_ARGS_GET = 0,
    PW_PY_WASI_ARGS_SIZES_GET = 1,
    PW_PY_WASI_CLOCK_RES_GET = 2,
    PW_PY_WASI_CLOCK_TIME_GET = 3,
    PW_PY_WASI_ENVIRON_GET = 4,
    PW_PY_WASI_ENVIRON_SIZES_GET = 5,
    PW_PY_WASI_FD_ADVISE = 6,
    PW_PY_WASI_FD_ALLOCATE = 7,
    PW_PY_WASI_FD_CLOSE = 8,
    PW_PY_WASI_FD_DATASYNC = 9,
    PW_PY_WASI_FD_FDSTAT_GET = 10,
    PW_PY_WASI_FD_FDSTAT_SET_FLAGS = 11,
    PW_PY_WASI_FD_FILESTAT_GET = 12,
    PW_PY_WASI_FD_FILESTAT_SET_SIZE = 13,
    PW_PY_WASI_FD_FILESTAT_SET_TIMES = 14,
    PW_PY_WASI_FD_PREAD = 15,
    PW_PY_WASI_FD_PRESTAT_DIR_NAME = 16,
    PW_PY_WASI_FD_PRESTAT_GET = 17,
    PW_PY_WASI_FD_PWRITE = 18,
    PW_PY_WASI_FD_READ = 19,
    PW_PY_WASI_FD_READDIR = 20,
    PW_PY_WASI_FD_SEEK = 21,
    PW_PY_WASI_FD_SYNC = 22,
    PW_PY_WASI_FD_TELL = 23,
    PW_PY_WASI_FD_WRITE = 24,
    PW_PY_WASI_PATH_CREATE_DIRECTORY = 25,
    PW_PY_WASI_PATH_FILESTAT_GET = 26,
    PW_PY_WASI_PATH_FILESTAT_SET_TIMES = 27,
    PW_PY_WASI_PATH_LINK = 28,
    PW_PY_WASI_PATH_OPEN = 29,
    PW_PY_WASI_PATH_READLINK = 30,
    PW_PY_WASI_PATH_REMOVE_DIRECTORY = 31,
    PW_PY_WASI_PATH_RENAME = 32,
    PW_PY_WASI_PATH_SYMLINK = 33,
    PW_PY_WASI_PATH_UNLINK_FILE = 34,
    PW_PY_WASI_POLL_ONEOFF = 35,
    PW_PY_WASI_PROC_EXIT = 36,
    PW_PY_WASI_RANDOM_GET = 37,
    PW_PY_WASI_SCHED_YIELD = 38,
    PW_PY_WASI_SOCK_ACCEPT = 39,
    PW_PY_WASI_SOCK_RECV = 40,
    PW_PY_WASI_SOCK_SEND = 41,
    PW_PY_WASI_SOCK_SHUTDOWN = 42
} pw_py_wasi_op_t;

/* Embedded immutable engine identity for consumer handshake verification. */
typedef struct pw_py_engine_identity {
    uint32_t abi_version;
    uint32_t import_count;
    const char* engine_tag;
    const char* source_commit;
    const char* platform;
    const char* cpython_version;
    const char* cpython_wasm_sha256;
    const char* metered_wasm_sha256;
    const char* wabt_version;
    const char* fuel_algorithm_version;
    const char* import_inventory_sha256;
} pw_py_engine_identity_t;

/* Per-instance resource and execution configuration. */
typedef struct pw_py_instance_config {
    uint32_t struct_size;      /* Must equal sizeof(pw_py_instance_config_t). */
    uint32_t max_memory_pages; /* Hard ceiling in 64 KiB pages (0 => 4096). */
    uint64_t fuel_budget;      /* Exact loop+func fuel limit (0 => unlimited). */
} pw_py_instance_config_t;

/* Outcome and accounting returned by pw_py_instance_run(). */
typedef struct pw_py_run_result {
    pw_py_status_t status;
    uint32_t exit_code;         /* Valid when status == PW_PY_STATUS_PROC_EXIT. */
    uint32_t trap_code;         /* WABT wasm_rt_trap_t code on trap. */
    uint32_t peak_memory_pages; /* High-water mark of linear memory pages. */
    int64_t fuel_remaining;     /* Remaining __pw_fuel (< 0 on exhaustion). */
    uint64_t guest_work_used;   /* Consumed fuel units (clamped on cancel). */
} pw_py_run_result_t;

/* Opaque handle for one isolated CPython/WASI guest instance. */
typedef struct pw_py_instance pw_py_instance_t;

/*
 * Host WASI import trampoline callback.
 *
 *  - host_ctx: opaque pointer supplied to pw_py_instance_new().
 *  - op_id:    canonical pw_py_wasi_op_t enum value (0..42).
 *  - op_name:  NUL-terminated WASI import name (e.g. "path_open").
 *  - args:     array of `argc` u64-widened Wasm arguments (i32 zero-extended).
 *  - argc:     number of arguments in `args`.
 *  - out_ret:  pointer where host writes u32 WASI errno (or exit code on EXIT).
 */
typedef pw_py_wasi_action_t (*pw_py_host_wasi_fn)(
    void* host_ctx,
    pw_py_wasi_op_t op_id,
    const char* op_name,
    const uint64_t* args,
    uint32_t argc,
    uint32_t* out_ret);

/* Returns static embedded engine identity metadata. Never returns NULL. */
PW_PY_API const pw_py_engine_identity_t* pw_py_engine_identity(void);

/* Idempotent, thread-safe one-time runtime initialization (pthread_once). */
PW_PY_API pw_py_status_t pw_py_engine_init(void);

/*
 * Allocates and instantiates a fresh isolated CPython/WASI guest instance.
 * Enforces cfg->max_memory_pages >= initial memory pages.
 */
PW_PY_API pw_py_status_t pw_py_instance_new(
    const pw_py_instance_config_t* cfg,
    pw_py_host_wasi_fn wasi_cb,
    void* host_ctx,
    pw_py_instance_t** out_inst);

/*
 * Executes the guest `_start` entry point inside a per-thread WABT trap guard.
 * Enforces single-thread affinity on `inst`.
 */
PW_PY_API pw_py_status_t pw_py_instance_run(
    pw_py_instance_t* inst,
    pw_py_run_result_t* out_res);

/*
 * Asynchronously cancels a running or about-to-run instance from any thread.
 * Interrupts pure CPU-only loops via atomic/volatile fuel exhaustion.
 */
PW_PY_API void pw_py_instance_cancel(pw_py_instance_t* inst);

/*
 * Inspects current guest linear memory pointer, byte length, current pages,
 * and configured max_pages ceiling. Safe to call from inside `wasi_cb` or
 * after `pw_py_instance_new()` / `pw_py_instance_run()`.
 */
PW_PY_API pw_py_status_t pw_py_instance_memory(
    pw_py_instance_t* inst,
    uint8_t** out_data,
    uint64_t* out_size,
    uint32_t* out_pages,
    uint32_t* out_max_pages);

/*
 * Releases all guest memory, tables, and instance state. NULL-safe.
 * Must not be called concurrently with pw_py_instance_run().
 */
PW_PY_API void pw_py_instance_free(pw_py_instance_t* inst);

#ifdef __cplusplus
}
#endif

#endif /* PW_PY_ENGINE_H */
