//go:build linux

// Package packagegeneration materializes complete immutable native runtimes.
package packagegeneration

const nativeStore = "/opt/painted-wolf-code/generations"
const lifetimeLocks = "/run/paintedwolf/generation-locks"

// Generation identifies completed bytes; it does not itself select a launch policy.
type Generation struct {
	Root               string
	ManifestSHA256     string
	LogicalInstallRoot string
}
