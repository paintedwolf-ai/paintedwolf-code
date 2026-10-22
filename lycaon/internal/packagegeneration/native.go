//go:build linux

// Package packagegeneration materializes complete immutable native runtimes.
package packagegeneration

import (
	"errors"
	"os"
)

const nativeStore = "/opt/painted-wolf-code/generations"
const lifetimeLocks = "/run/paintedwolf/generation-locks"

// Generation identifies completed bytes; it does not itself select a launch policy.
type Generation struct {
	Root               string
	ManifestSHA256     string
	LogicalInstallRoot string
}

// Materialize copies a package-owned staging tree into independently retained bytes.
// Only the root installation transaction may call this fixed native-store operation.
func Materialize(staging *os.Root, manifest []byte) (Generation, error) {
	if os.Geteuid() != 0 {
		return Generation{}, errors.New("native generation: installation requires root")
	}
	return native.materialize(staging, manifest)
}
