//go:build !linux

package installbinding

// Binding is the captured running installation; only Linux packages can be
// replaced underneath a running engine.
type Binding struct{}

// Install is a no-op where no package can replace the running installation.
func Install(*Binding) {}

// Verify accepts every launch where no package can replace the running installation.
func Verify(string) error { return nil }

// CaptureRunning has nothing to capture outside a Linux package installation.
func CaptureRunning() (*Binding, error) { return nil, nil }
