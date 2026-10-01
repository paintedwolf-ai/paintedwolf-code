package llm

import (
	"os"
	"runtime"
	"strings"
)

// Platform tokens are shared by provider configuration and the wire contract.
const (
	PlatformmacOS   = "macos"
	PlatformWindows = "windows"
	PlatformLinux   = "linux"
)

// hostProductPlatform maps runtime.GOOS to a product platform token.
// Overridable in tests via SetHostProductPlatformForTest.
var hostProductPlatform = defaultHostProductPlatform

func defaultHostProductPlatform() string {
	switch runtime.GOOS {
	case "darwin":
		return PlatformmacOS
	case "windows":
		return PlatformWindows
	case "linux":
		return PlatformLinux
	default:
		return runtime.GOOS
	}
}

// SetHostProductPlatformForTest overrides the resolver during tests.
func SetHostProductPlatformForTest(platform string) (restore func()) {
	if os.Getenv("LYCAON_TEST") != "1" {
		panic("llm.SetHostProductPlatformForTest requires LYCAON_TEST=1")
	}
	prev := hostProductPlatform
	hostProductPlatform = func() string { return platform }
	return func() { hostProductPlatform = prev }
}

// HostProductPlatform returns the sidecar host's product platform token.
func HostProductPlatform() string {
	return hostProductPlatform()
}

// PlatformsSupported treats an empty list as unrestricted.
func PlatformsSupported(platforms []string, host string) bool {
	if len(platforms) == 0 {
		return true
	}
	host = strings.TrimSpace(host)
	for _, p := range platforms {
		if strings.TrimSpace(p) == host {
			return true
		}
	}
	return false
}

// PlatformsSupportedHere checks the current host.
func PlatformsSupportedHere(platforms []string) bool {
	return PlatformsSupported(platforms, HostProductPlatform())
}
