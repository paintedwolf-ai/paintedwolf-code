package pythonruntime

import (
	"context"
	"fmt"
	"sort"

	"github.com/tetratelabs/wazero"
)

// ImportDisposition records the host policy for a Wasm binary import.
type ImportDisposition string

const (
	ImportAllowed  ImportDisposition = "allowed"
	ImportMediated ImportDisposition = "mediated"
	ImportDenied   ImportDisposition = "denied"
)

// WasmImportSpec declares a single imported Wasm function and its host
// disposition.
type WasmImportSpec struct {
	Module      string            `json:"module"`
	Name        string            `json:"name"`
	Kind        string            `json:"kind,omitempty"`
	Disposition ImportDisposition `json:"disposition"`
	Authority   string            `json:"authority,omitempty"`
	FailureMode string            `json:"failure_mode,omitempty"`
}

// ImportAuditReport is the result of auditing a compiled Wasm module's actual
// imported functions against a declared disposition policy.
type ImportAuditReport struct {
	Observed   []WasmImportSpec `json:"observed"`
	Allowed    []WasmImportSpec `json:"allowed,omitempty"`
	Mediated   []WasmImportSpec `json:"mediated,omitempty"`
	Denied     []WasmImportSpec `json:"denied,omitempty"`
	Undeclared []WasmImportSpec `json:"undeclared,omitempty"`
	Passed     bool             `json:"passed"`
}

// DefaultWASIImportPolicy returns the baseline disposition table for
// wasi_snapshot_preview1 imports used by CPython/WASI.
func DefaultWASIImportPolicy() []WasmImportSpec {
	const mod = "wasi_snapshot_preview1"
	allowed := []string{
		"args_get",
		"args_sizes_get",
		"clock_res_get",
		"clock_time_get",
		"environ_get",
		"environ_sizes_get",
		"fd_advise",
		"fd_close",
		"fd_fdstat_get",
		"fd_fdstat_set_flags",
		"fd_filestat_get",
		"fd_prestat_dir_name",
		"fd_prestat_get",
		"fd_read",
		"fd_readdir",
		"fd_seek",
		"fd_tell",
		"fd_write",
		"poll_oneoff",
		"proc_exit",
		"random_get",
		"sched_yield",
	}
	mediated := []string{
		"fd_allocate",
		"fd_datasync",
		"fd_filestat_set_size",
		"fd_filestat_set_times",
		"fd_pread",
		"fd_pwrite",
		"fd_renumber",
		"fd_sync",
		"path_create_directory",
		"path_filestat_get",
		"path_filestat_set_times",
		"path_link",
		"path_open",
		"path_readlink",
		"path_remove_directory",
		"path_rename",
		"path_symlink",
		"path_unlink_file",
	}
	// Socket WASI imports are statically linked by wasi-libc in CPython 3.12;
	// zero socket preopens are granted, so wazero returns ENOSYS/ENOTSUP.
	noAuthoritySockets := []string{
		"sock_accept",
		"sock_recv",
		"sock_send",
		"sock_shutdown",
	}
	denied := []string{
		"proc_raise",
	}

	specs := make([]WasmImportSpec, 0, len(allowed)+len(mediated)+len(noAuthoritySockets)+len(denied))
	for _, name := range allowed {
		specs = append(specs, WasmImportSpec{Module: mod, Name: name, Kind: "func", Disposition: ImportAllowed, Authority: "runtime"})
	}
	for _, name := range mediated {
		specs = append(specs, WasmImportSpec{Module: mod, Name: name, Kind: "func", Disposition: ImportMediated, Authority: "workspace_adapter"})
	}
	for _, name := range noAuthoritySockets {
		specs = append(specs, WasmImportSpec{Module: mod, Name: name, Kind: "func", Disposition: ImportMediated, Authority: "none", FailureMode: "ENOSYS/ENOTSUP (no preopened sockets)"})
	}
	for _, name := range denied {
		specs = append(specs, WasmImportSpec{Module: mod, Name: name, Kind: "func", Disposition: ImportDenied, Authority: "none", FailureMode: "rejected at module load"})
	}
	return specs
}

// AuditModuleImports compiles wasmBytes with wazero and verifies every actual
// imported function against policy.
func AuditModuleImports(ctx context.Context, wasmBytes []byte, policy []WasmImportSpec) (*ImportAuditReport, error) {
	if len(wasmBytes) == 0 {
		return nil, fmt.Errorf("audit module imports: empty wasm binary")
	}
	rt := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfigInterpreter())
	defer rt.Close(ctx)

	compiled, err := rt.CompileModule(ctx, wasmBytes)
	if err != nil {
		return nil, fmt.Errorf("compile wasm for import audit: %w", err)
	}
	return AuditCompiledModule(compiled, policy), nil
}

// AuditCompiledModule inspects an already-compiled wazero module's imported
// functions against policy.
func AuditCompiledModule(compiled wazero.CompiledModule, policy []WasmImportSpec) *ImportAuditReport {
	type key struct {
		module string
		name   string
	}
	index := make(map[key]WasmImportSpec, len(policy))
	for _, spec := range policy {
		index[key{module: spec.Module, name: spec.Name}] = spec
	}

	report := &ImportAuditReport{Passed: true}
	imports := compiled.ImportedFunctions()
	for _, fn := range imports {
		modName, fnName, ok := fn.Import()
		if !ok {
			continue
		}
		spec, declared := index[key{module: modName, name: fnName}]
		if !declared {
			undeclared := WasmImportSpec{Module: modName, Name: fnName, Disposition: ImportDenied}
			report.Observed = append(report.Observed, undeclared)
			report.Undeclared = append(report.Undeclared, undeclared)
			report.Passed = false
			continue
		}
		report.Observed = append(report.Observed, spec)
		switch spec.Disposition {
		case ImportAllowed:
			report.Allowed = append(report.Allowed, spec)
		case ImportMediated:
			report.Mediated = append(report.Mediated, spec)
		default:
			report.Denied = append(report.Denied, spec)
			report.Passed = false
		}
	}

	sortImportSpecs(report.Observed)
	sortImportSpecs(report.Allowed)
	sortImportSpecs(report.Mediated)
	sortImportSpecs(report.Denied)
	sortImportSpecs(report.Undeclared)
	return report
}

func sortImportSpecs(specs []WasmImportSpec) {
	sort.Slice(specs, func(i, j int) bool {
		if specs[i].Module != specs[j].Module {
			return specs[i].Module < specs[j].Module
		}
		return specs[i].Name < specs[j].Name
	})
}
