//go:build !cgo || (!darwin && !linux)

package pythonruntime

import "context"

// NewNativeEngineRuntime fails closed when cgo is disabled (e.g. static Linux
// CGO_ENABLED=0 builds) or on unsupported operating systems. It NEVER silently
// falls back to wazero.
func NewNativeEngineRuntime(_ context.Context, _ RuntimeConfig) (Runtime, error) {
	return nil, &NativeEngineVerificationError{
		Stage:   "build",
		Message: "native portable python AOT engine requires CGO_ENABLED=1 on darwin or linux",
	}
}
