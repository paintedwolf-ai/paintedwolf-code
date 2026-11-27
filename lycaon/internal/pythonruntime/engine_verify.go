package pythonruntime

import (
	"fmt"
	"strings"
)

// HandshakeVerificationInput carries all inputs required to perform the
// fail-closed pre-execution verification of a native engine library against
// the bundled release manifest, its runtime pw_py_engine_identity() response,
// and the paired Portable Python WASI image.
type HandshakeVerificationInput struct {
	BundledManifest  *BundledEngineManifest
	EngineDir        string
	ImageDir         string
	ImageManifest    *ImageManifest
	ReportedIdentity *NativeEngineIdentity
	GOOS             string
	GOARCH           string
}

// VerifyNativeEngineHandshake performs complete fail-closed verification BEFORE
// any guest instantiation:
//  1. Validates BundledEngineManifest and verifies the staged shared library's
//     regular file status, byte length, SHA-256, and artifact-manifest.json.
//  2. Verifies pw_py_engine_identity() fields: abi_version, import_count,
//     platform, cpython_wasm_sha256, metered_wasm_sha256, and
//     import_inventory_sha256.
//  3. Verifies ImageManifest and full image integrity (including checked-hash
//     CPython 3.12 .pyc bytecode if present) and confirms exact pairing between
//     the native engine and image digests.
func VerifyNativeEngineHandshake(in HandshakeVerificationInput) (*NativeEngineIdentity, error) {
	if in.BundledManifest == nil {
		return nil, &NativeEngineVerificationError{
			Stage:   "manifest",
			Message: "bundled native engine manifest is nil",
		}
	}
	if err := ValidateBundledEngineManifest(in.BundledManifest); err != nil {
		return nil, &NativeEngineVerificationError{
			Stage:   "manifest",
			Message: "bundled native engine manifest is invalid or unpinned",
			Err:     err,
		}
	}
	art, err := in.BundledManifest.ArtifactForPlatform(in.GOOS, in.GOARCH)
	if err != nil {
		return nil, &NativeEngineVerificationError{
			Stage:   "platform",
			Message: fmt.Sprintf("no pinned native engine artifact for %s/%s", in.GOOS, in.GOARCH),
			Err:     err,
		}
	}
	libPath, err := VerifyEngineDir(in.BundledManifest, in.EngineDir, in.GOOS, in.GOARCH, art)
	if err != nil {
		return nil, &NativeEngineVerificationError{
			Stage:   "artifact",
			Message: "staged native engine library or artifact-manifest.json failed integrity check",
			Err:     err,
		}
	}
	if in.ReportedIdentity == nil {
		return nil, &NativeEngineVerificationError{
			Stage:   "abi",
			Message: "pw_py_engine_identity() returned nil",
		}
	}
	id := *in.ReportedIdentity
	id.LibraryPath = libPath
	id.LibrarySHA256 = strings.ToLower(strings.TrimSpace(art.SHA256))

	if id.ABIVersion != ExpectedNativeEngineABIVersion {
		return nil, &NativeEngineVerificationError{
			Stage:   "abi",
			Message: fmt.Sprintf("abi_version mismatch: engine reported %d, host requires %d", id.ABIVersion, ExpectedNativeEngineABIVersion),
		}
	}
	if id.ImportCount != ExpectedNativeEngineImportCount {
		return nil, &NativeEngineVerificationError{
			Stage:   "import_inventory",
			Message: fmt.Sprintf("import_count mismatch: engine reported %d, host requires %d", id.ImportCount, ExpectedNativeEngineImportCount),
		}
	}
	wantPlatform := in.GOOS + "-" + in.GOARCH
	if id.Platform != wantPlatform {
		return nil, &NativeEngineVerificationError{
			Stage:   "platform",
			Message: fmt.Sprintf("platform mismatch: engine reported %q, host requires %q", id.Platform, wantPlatform),
		}
	}
	if !strings.EqualFold(id.CPythonWasmSHA256, in.BundledManifest.Engine.CPythonWasmSHA256) {
		return nil, &NativeEngineVerificationError{
			Stage:   "cpython_wasm",
			Message: fmt.Sprintf("engine cpython_wasm_sha256 %s does not match bundled manifest %s", id.CPythonWasmSHA256, in.BundledManifest.Engine.CPythonWasmSHA256),
		}
	}
	if !strings.EqualFold(id.MeteredWasmSHA256, in.BundledManifest.Engine.MeteredWasmSHA256) {
		return nil, &NativeEngineVerificationError{
			Stage:   "metered_wasm",
			Message: fmt.Sprintf("engine metered_wasm_sha256 %s does not match bundled manifest %s", id.MeteredWasmSHA256, in.BundledManifest.Engine.MeteredWasmSHA256),
		}
	}
	if !strings.EqualFold(id.ImportInventorySHA256, in.BundledManifest.Engine.ImportInventorySHA256) ||
		!strings.EqualFold(id.ImportInventorySHA256, ExpectedNativeEngineImportInventorySHA256) {
		return nil, &NativeEngineVerificationError{
			Stage:   "import_inventory",
			Message: fmt.Sprintf("engine import_inventory_sha256 %s does not match expected %s", id.ImportInventorySHA256, ExpectedNativeEngineImportInventorySHA256),
		}
	}

	if in.ImageManifest == nil {
		return nil, &NativeEngineVerificationError{
			Stage:   "image",
			Message: "paired image manifest is nil",
		}
	}
	if !strings.EqualFold(id.CPythonWasmSHA256, in.ImageManifest.Interpreter.WasmSHA256) {
		return nil, &NativeEngineVerificationError{
			Stage:   "cpython_wasm",
			Message: fmt.Sprintf("engine/image skew: engine cpython_wasm_sha256 %s != image %s", id.CPythonWasmSHA256, in.ImageManifest.Interpreter.WasmSHA256),
		}
	}
	if strings.TrimSpace(in.ImageManifest.Interpreter.MeteredWasmSHA256) != "" &&
		!strings.EqualFold(id.MeteredWasmSHA256, in.ImageManifest.Interpreter.MeteredWasmSHA256) {
		return nil, &NativeEngineVerificationError{
			Stage:   "metered_wasm",
			Message: fmt.Sprintf("engine/image skew: engine metered_wasm_sha256 %s != image %s", id.MeteredWasmSHA256, in.ImageManifest.Interpreter.MeteredWasmSHA256),
		}
	}
	if strings.TrimSpace(in.ImageManifest.ImportInventorySHA256) != "" &&
		!strings.EqualFold(id.ImportInventorySHA256, in.ImageManifest.ImportInventorySHA256) {
		return nil, &NativeEngineVerificationError{
			Stage:   "import_inventory",
			Message: fmt.Sprintf("engine/image skew: engine import_inventory_sha256 %s != image %s", id.ImportInventorySHA256, in.ImageManifest.ImportInventorySHA256),
		}
	}
	if strings.TrimSpace(in.ImageDir) != "" {
		if err := VerifyImage(in.ImageDir, in.ImageManifest); err != nil {
			return nil, &NativeEngineVerificationError{
				Stage:   "image",
				Message: "paired portable python image failed integrity/bytecode verification",
				Err:     err,
			}
		}
	}
	return &id, nil
}
