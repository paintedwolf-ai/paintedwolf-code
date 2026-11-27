//go:build pw_native_python

package pythonruntime

// DefaultBackendKind returns BackendNativeAOT when built with -tags pw_native_python.
func DefaultBackendKind() BackendKind {
	return BackendNativeAOT
}
