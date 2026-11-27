//go:build !pw_native_python

package pythonruntime

import "runtime"

// DefaultBackendKind returns the build-configured default backend when the
// explicit pw_native_python build tag is not set.
//
// Linux defaults to wazero-compiler (preserving static CGO_ENABLED=0 sidecar
// builds). macOS uses wazero-interpreter until PY07's signed darwin-arm64
// native engine release is pinned in bundled-manifest.yaml, at which point
// darwin switches to BackendNativeAOT.
func DefaultBackendKind() BackendKind {
	if runtime.GOOS == "darwin" {
		return BackendWazeroInterpreter
	}
	return BackendWazeroCompiler
}
