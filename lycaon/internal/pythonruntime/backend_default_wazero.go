//go:build !cgo || (!darwin && !linux) || pw_wazero_python

package pythonruntime

import "runtime"

// DefaultBackendKind returns the wazero control backend when built without cgo
// or with -tags pw_wazero_python. Requesting BackendNativeAOT explicitly still
// fails closed without silent fallback.
func DefaultBackendKind() BackendKind {
	if runtime.GOOS == "darwin" {
		return BackendWazeroInterpreter
	}
	return BackendWazeroCompiler
}
