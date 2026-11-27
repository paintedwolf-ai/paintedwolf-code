package pythonruntime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

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
	imports := compiled.ImportedFunctions()
	observed := make([]WasmImportSpec, 0, len(imports))
	for _, fn := range imports {
		modName, fnName, ok := fn.Import()
		if !ok {
			continue
		}
		observed = append(observed, WasmImportSpec{Module: modName, Name: fnName, Kind: "func"})
	}
	return AuditObservedImports(observed, policy)
}

// AuditObservedImports classifies an observed list of Wasm imports against a
// declared disposition policy. Any observed import that is absent from policy
// is recorded in Undeclared and fails the audit; any observed import whose
// policy disposition is neither allowed nor mediated is recorded in Denied and
// fails the audit.
func AuditObservedImports(observed []WasmImportSpec, policy []WasmImportSpec) *ImportAuditReport {
	type key struct {
		module string
		name   string
	}
	index := make(map[key]WasmImportSpec, len(policy))
	for _, spec := range policy {
		index[key{module: spec.Module, name: spec.Name}] = spec
	}

	report := &ImportAuditReport{Passed: true}
	for _, obs := range observed {
		spec, declared := index[key{module: obs.Module, name: obs.Name}]
		if !declared {
			undeclared := WasmImportSpec{Module: obs.Module, Name: obs.Name, Kind: obs.Kind, Disposition: ImportDenied}
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

// ParseWasmImportedFunctions parses the Wasm binary Import Section (section ID 2)
// and returns every imported function without compiling the module.
func ParseWasmImportedFunctions(wasmBytes []byte) ([]WasmImportSpec, error) {
	if len(wasmBytes) < 8 || !bytes.Equal(wasmBytes[:4], []byte{0x00, 0x61, 0x73, 0x6d}) {
		return nil, fmt.Errorf("invalid wasm binary header")
	}
	pos := 8
	for pos < len(wasmBytes) {
		secID := wasmBytes[pos]
		pos++
		secSize, next, err := readWasmULEB128(wasmBytes, pos)
		if err != nil {
			return nil, fmt.Errorf("read wasm section %d size: %w", secID, err)
		}
		pos = next
		end := pos + int(secSize)
		if end < pos || end > len(wasmBytes) {
			return nil, fmt.Errorf("wasm section %d exceeds binary length", secID)
		}
		if secID != 2 {
			pos = end
			continue
		}
		count, cur, err := readWasmULEB128(wasmBytes, pos)
		if err != nil {
			return nil, fmt.Errorf("read wasm import count: %w", err)
		}
		out := make([]WasmImportSpec, 0, count)
		for i := uint32(0); i < count; i++ {
			modName, nextCur, err := readWasmName(wasmBytes, cur, end)
			if err != nil {
				return nil, fmt.Errorf("read wasm import %d module: %w", i, err)
			}
			cur = nextCur
			fnName, nextCur, err := readWasmName(wasmBytes, cur, end)
			if err != nil {
				return nil, fmt.Errorf("read wasm import %d name: %w", i, err)
			}
			cur = nextCur
			if cur >= end {
				return nil, fmt.Errorf("truncated wasm import %d kind", i)
			}
			kind := wasmBytes[cur]
			cur++
			switch kind {
			case 0x00: // func: typeidx
				_, cur, err = readWasmULEB128(wasmBytes, cur)
				if err != nil {
					return nil, fmt.Errorf("read wasm func import %d typeidx: %w", i, err)
				}
				out = append(out, WasmImportSpec{Module: modName, Name: fnName, Kind: "func"})
			case 0x01: // table: reftype + limits
				if cur >= end {
					return nil, fmt.Errorf("truncated wasm table import %d", i)
				}
				cur++ // reftype
				cur, err = skipWasmLimits(wasmBytes, cur, end)
				if err != nil {
					return nil, fmt.Errorf("skip wasm table import %d limits: %w", i, err)
				}
			case 0x02: // memory: limits
				cur, err = skipWasmLimits(wasmBytes, cur, end)
				if err != nil {
					return nil, fmt.Errorf("skip wasm memory import %d limits: %w", i, err)
				}
			case 0x03: // global: valtype + mut
				if cur+2 > end {
					return nil, fmt.Errorf("truncated wasm global import %d", i)
				}
				cur += 2
			default:
				return nil, fmt.Errorf("unsupported wasm import %d kind 0x%02x", i, kind)
			}
		}
		sortImportSpecs(out)
		return out, nil
	}
	return nil, nil
}

func readWasmULEB128(b []byte, pos int) (uint32, int, error) {
	var result uint32
	var shift uint
	for {
		if pos >= len(b) {
			return 0, pos, fmt.Errorf("unexpected EOF in LEB128")
		}
		v := b[pos]
		pos++
		result |= uint32(v&0x7f) << shift
		if v&0x80 == 0 {
			return result, pos, nil
		}
		shift += 7
		if shift >= 35 {
			return 0, pos, fmt.Errorf("LEB128 overflow")
		}
	}
}

func readWasmName(b []byte, pos, end int) (string, int, error) {
	n, next, err := readWasmULEB128(b, pos)
	if err != nil {
		return "", pos, err
	}
	strEnd := next + int(n)
	if strEnd < next || strEnd > end {
		return "", next, fmt.Errorf("wasm name exceeds section bounds")
	}
	return string(b[next:strEnd]), strEnd, nil
}

func skipWasmLimits(b []byte, pos, end int) (int, error) {
	if pos >= end {
		return pos, fmt.Errorf("truncated wasm limits")
	}
	flags := b[pos]
	pos++
	var err error
	_, pos, err = readWasmULEB128(b, pos)
	if err != nil {
		return pos, err
	}
	if flags&0x01 != 0 {
		_, pos, err = readWasmULEB128(b, pos)
		if err != nil {
			return pos, err
		}
	}
	if pos > end {
		return pos, fmt.Errorf("wasm limits exceed section bounds")
	}
	return pos, nil
}

type nativeImportInventoryFile struct {
	SchemaVersion string `json:"schema_version"`
	ImportCount   uint32 `json:"import_count"`
	Imports       []struct {
		Module string `json:"module"`
		Name   string `json:"name"`
	} `json:"imports"`
}

// LoadVerifiedImportInventory reads an imports.json file, verifies its SHA-256
// and import count against the verified native engine identity, and returns its
// observed import list.
func LoadVerifiedImportInventory(path, expectedSHA256 string, expectedCount uint32) ([]WasmImportSpec, error) {
	// #nosec G304 -- path is resolved inside verified engineDir or test fixture.
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(raw)
	gotSHA := hex.EncodeToString(sum[:])
	if expectedSHA256 != "" && !strings.EqualFold(gotSHA, expectedSHA256) {
		return nil, fmt.Errorf("imports.json sha256 mismatch: got %s want %s", gotSHA, expectedSHA256)
	}
	var inv nativeImportInventoryFile
	if err := json.Unmarshal(raw, &inv); err != nil {
		return nil, fmt.Errorf("parse imports.json: %w", err)
	}
	if expectedCount > 0 && uint32(len(inv.Imports)) != expectedCount {
		return nil, fmt.Errorf("imports.json count mismatch: got %d want %d", len(inv.Imports), expectedCount)
	}
	out := make([]WasmImportSpec, 0, len(inv.Imports))
	for _, item := range inv.Imports {
		out = append(out, WasmImportSpec{Module: item.Module, Name: item.Name, Kind: "func"})
	}
	sortImportSpecs(out)
	return out, nil
}

// AuditNativeEngineImports derives the observed import list from the verified
// engine's actual paired Wasm module (and/or verified imports.json inventory),
// cross-checks count and digests against verifiedID, and applies policy.
func AuditNativeEngineImports(
	verifiedID *NativeEngineIdentity,
	engineDir string,
	imageDir string,
	manifest *ImageManifest,
	wasmBytes []byte,
	policy []WasmImportSpec,
) (*ImportAuditReport, error) {
	if verifiedID == nil {
		return nil, &NativeEngineVerificationError{
			Stage:   "import_inventory",
			Message: "cannot audit native engine imports without verified identity",
		}
	}

	var (
		observedFromWasm []WasmImportSpec
		observedFromInv  []WasmImportSpec
	)

	// 1. If Wasm bytes are provided (or resolvable from the verified imageDir),
	// verify SHA-256 == verifiedID.CPythonWasmSHA256 and parse the actual Wasm
	// Import Section.
	if len(wasmBytes) == 0 && strings.TrimSpace(imageDir) != "" && manifest != nil {
		if wasmPath, _, _, err := ResolveImagePaths(imageDir, manifest); err == nil {
			// #nosec G304 -- wasmPath resolved inside verified imageDir.
			if raw, rErr := os.ReadFile(wasmPath); rErr == nil {
				wasmBytes = raw
			}
		}
	}
	if len(wasmBytes) > 0 {
		sum := sha256.Sum256(wasmBytes)
		gotSHA := hex.EncodeToString(sum[:])
		if !strings.EqualFold(gotSHA, verifiedID.CPythonWasmSHA256) {
			return nil, &NativeEngineVerificationError{
				Stage:   "cpython_wasm",
				Message: fmt.Sprintf("wasm bytes sha256 %s does not match verified engine %s", gotSHA, verifiedID.CPythonWasmSHA256),
			}
		}
		parsed, err := ParseWasmImportedFunctions(wasmBytes)
		if err != nil {
			return nil, &NativeEngineVerificationError{
				Stage:   "import_inventory",
				Message: "failed to parse verified python.wasm import section",
				Err:     err,
			}
		}
		if uint32(len(parsed)) != verifiedID.ImportCount {
			return nil, &NativeEngineVerificationError{
				Stage:   "import_inventory",
				Message: fmt.Sprintf("python.wasm import count %d does not match engine identity %d", len(parsed), verifiedID.ImportCount),
			}
		}
		observedFromWasm = parsed
	}

	// 2. If imports.json is present in engineDir, verify its SHA-256 and count
	// against verifiedID and cross-check it against parsed Wasm imports.
	if strings.TrimSpace(engineDir) != "" {
		invPath := filepath.Join(engineDir, "imports.json")
		if info, err := os.Stat(invPath); err == nil && !info.IsDir() {
			parsedInv, err := LoadVerifiedImportInventory(invPath, verifiedID.ImportInventorySHA256, verifiedID.ImportCount)
			if err != nil {
				return nil, &NativeEngineVerificationError{
					Stage:   "import_inventory",
					Message: "engine imports.json failed verification",
					Err:     err,
				}
			}
			observedFromInv = parsedInv
		}
	}

	if len(observedFromWasm) > 0 && len(observedFromInv) > 0 {
		if !sameImportNames(observedFromWasm, observedFromInv) {
			return nil, &NativeEngineVerificationError{
				Stage:   "import_inventory",
				Message: "python.wasm observed imports do not match verified imports.json inventory",
			}
		}
	}

	observed := observedFromWasm
	if len(observed) == 0 {
		observed = observedFromInv
	}
	if len(observed) == 0 {
		return nil, &NativeEngineVerificationError{
			Stage:   "import_inventory",
			Message: "no verified python.wasm or imports.json available to audit native engine imports",
		}
	}

	if len(policy) == 0 && manifest != nil && len(manifest.WasmImports) > 0 {
		policy = manifest.WasmImports
	}
	if len(policy) == 0 {
		policy = DefaultWASIImportPolicy()
	}
	return AuditObservedImports(observed, policy), nil
}

func sameImportNames(a, b []WasmImportSpec) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Module != b[i].Module || a[i].Name != b[i].Name {
			return false
		}
	}
	return true
}

func sortImportSpecs(specs []WasmImportSpec) {
	sort.Slice(specs, func(i, j int) bool {
		if specs[i].Module != specs[j].Module {
			return specs[i].Module < specs[j].Module
		}
		return specs[i].Name < specs[j].Name
	})
}
