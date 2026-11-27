//go:build cgo && (darwin || linux) && !pw_wazero_python

package pythonruntime

// DefaultBackendKind returns BackendNativeAOT on both darwin/arm64 and
// linux/amd64 product builds. Previous wazero backends remain available as
// explicit test/benchmark controls via ServiceConfig.Backend or -tags pw_wazero_python.
func DefaultBackendKind() BackendKind {
	return BackendNativeAOT
}
