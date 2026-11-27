package pythonruntime

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/pythonnative"
)

// BackendKind identifies the execution backend hosting CPython/WASI.
type BackendKind string

const (
	// BackendWazeroCompiler executes CPython/WASI with wazero's AOT compiler (Linux default).
	BackendWazeroCompiler BackendKind = "wazero-compiler"
	// BackendWazeroInterpreter executes CPython/WASI with wazero's interpreter.
	BackendWazeroInterpreter BackendKind = "wazero-interpreter"
	// BackendNativeAOT executes PY07's precompiled native CPython/WASI shared library.
	BackendNativeAOT BackendKind = "native-aot"
)

// NativeEngineIdentity is the Go representation of pw_py_engine_identity_t
// reported by libpw_cpython_wasi.{dylib,so} via pw_py_engine_identity().
type NativeEngineIdentity struct {
	ABIVersion            uint32 `json:"abi_version"`
	ImportCount           uint32 `json:"import_count"`
	EngineTag             string `json:"engine_tag"`
	SourceCommit          string `json:"source_commit"`
	Platform              string `json:"platform"`
	CPythonVersion        string `json:"cpython_version"`
	CPythonWasmSHA256     string `json:"cpython_wasm_sha256"`
	MeteredWasmSHA256     string `json:"metered_wasm_sha256"`
	WABTVersion           string `json:"wabt_version"`
	FuelAlgorithmVersion  string `json:"fuel_algorithm_version"`
	ImportInventorySHA256 string `json:"import_inventory_sha256"`
	LibraryPath           string `json:"library_path,omitempty"`
	LibrarySHA256         string `json:"library_sha256,omitempty"`
}

// RuntimeInfo reports the active execution backend and, when running on the
// native AOT engine, its verified embedded identity.
type RuntimeInfo struct {
	Backend      BackendKind           `json:"backend"`
	NativeEngine *NativeEngineIdentity `json:"native_engine,omitempty"`
}

// NativeEngineVerificationError is returned when pre-execution verification of
// the native engine artifact, ABI handshake, or image pairing fails. Callers
// MUST treat this as fatal and NEVER silently fall back to wazero.
type NativeEngineVerificationError struct {
	Stage   string
	Message string
	Err     error
}

func (e *NativeEngineVerificationError) Error() string {
	if e == nil {
		return ""
	}
	if e.Err != nil {
		return fmt.Sprintf("portable python native engine verification failed [%s]: %s: %v", e.Stage, e.Message, e.Err)
	}
	return fmt.Sprintf("portable python native engine verification failed [%s]: %s", e.Stage, e.Message)
}

func (e *NativeEngineVerificationError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// DefaultBackendKind returns BackendNativeAOT unconditionally. When a build
// lacks native AOT capability (e.g. CGO_ENABLED=0 or an unsupported OS/arch),
// NewConfiguredRuntime fails explicitly rather than silently falling back to
// wazero. Benchmark and regression controls select wazero explicitly via
// RuntimeConfig.Backend.
func DefaultBackendKind() BackendKind {
	return BackendNativeAOT
}

func resolveWazeroBackend(requested BackendKind) (BackendKind, error) {
	switch requested {
	case "":
		if pythonnative.ReferenceWazeroUsesInterpreter() {
			return BackendWazeroInterpreter, nil
		}
		return BackendWazeroCompiler, nil
	case BackendWazeroCompiler, BackendWazeroInterpreter:
		return requested, nil
	default:
		return "", fmt.Errorf("wazero runtime does not support backend %q", requested)
	}
}

// NewConfiguredRuntime constructs the explicitly requested backend (or
// DefaultBackendKind when cfg.Backend is empty). It NEVER silently falls back
// between backends.
func NewConfiguredRuntime(ctx context.Context, cfg RuntimeConfig) (Runtime, error) {
	backend := cfg.Backend
	if backend == "" {
		backend = DefaultBackendKind()
	}
	switch backend {
	case BackendNativeAOT:
		return NewNativeEngineRuntime(ctx, cfg)
	case BackendWazeroCompiler, BackendWazeroInterpreter:
		return NewWazeroRuntime(ctx, cfg)
	default:
		return nil, fmt.Errorf("unsupported portable python runtime backend %q", backend)
	}
}

// DiscoverEngineDir locates the staged native engine directory containing
// the platform's verified native shared library and artifact-manifest.json.
func DiscoverEngineDir(engineRoot string) (string, error) {
	goos, goarch := pythonnative.HostPlatform()
	libName, err := LibraryNameForPlatform(goos, goarch)
	if err != nil {
		return "", &NativeEngineVerificationError{
			Stage:   "platform",
			Message: fmt.Sprintf("unsupported host platform %s/%s for native python engine", goos, goarch),
			Err:     err,
		}
	}

	if envDir := strings.TrimSpace(os.Getenv(EnvEngineDir)); envDir != "" {
		if hasEngineArtifacts(envDir, libName) {
			return filepath.Abs(envDir)
		}
		return "", &NativeEngineVerificationError{
			Stage:   "discover",
			Message: fmt.Sprintf("%s=%q does not contain %s and artifact-manifest.json", EnvEngineDir, envDir, libName),
		}
	}

	var candidates []string
	if strings.TrimSpace(engineRoot) != "" {
		candidates = append(candidates, StagedEngineDir(engineRoot))
	}
	if exe, err := os.Executable(); err == nil && exe != "" {
		exeDir := filepath.Dir(exe)
		candidates = append(candidates,
			filepath.Join(exeDir, "portable-python", "engine"),
			filepath.Join(exeDir, "..", "Resources", "engine-root", "portable-python", "engine"),
			filepath.Join(exeDir, "..", "share", "paintedwolf", "portable-python", "engine"),
		)
	}
	for _, cand := range candidates {
		if hasEngineArtifacts(cand, libName) {
			return filepath.Abs(cand)
		}
	}
	return "", &NativeEngineVerificationError{
		Stage:   "discover",
		Message: fmt.Sprintf("staged native python engine not found (checked %s and %d standard locations)", EnvEngineDir, len(candidates)),
	}
}

func hasEngineArtifacts(dir, libName string) bool {
	if strings.TrimSpace(dir) == "" || strings.TrimSpace(libName) == "" {
		return false
	}
	libInfo, err := os.Stat(filepath.Join(dir, libName))
	if err != nil || libInfo.IsDir() {
		return false
	}
	manInfo, err := os.Stat(filepath.Join(dir, "artifact-manifest.json"))
	return err == nil && !manInfo.IsDir()
}
