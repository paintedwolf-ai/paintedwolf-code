package pythonnative

import (
	"fmt"
	"slices"
	"strings"
)

// TargetSpec declares all platform-specific packaging and signing facts for
// one shipping Portable Python native AOT target.
type TargetSpec struct {
	GOOS                    string
	GOARCH                  string
	TripleAliases           []string
	LibraryName             string
	RequiresHardenedSigning bool
}

// shippingTargets is the single declared table of supported Portable Python
// native AOT shipping targets, their triple aliases, library filenames, and
// hardened-signing policy.
var shippingTargets = []TargetSpec{
	{
		GOOS:                    "darwin",
		GOARCH:                  "arm64",
		TripleAliases:           []string{"aarch64-apple-darwin", "darwin-arm64", "darwin/arm64"},
		LibraryName:             "libpw_cpython_wasi.dylib",
		RequiresHardenedSigning: true,
	},
	{
		GOOS:                    "linux",
		GOARCH:                  "amd64",
		TripleAliases:           []string{"x86_64-unknown-linux-gnu", "linux-amd64", "linux/amd64"},
		LibraryName:             "libpw_cpython_wasi.so",
		RequiresHardenedSigning: false,
	},
}

func cloneTargetSpec(spec TargetSpec) TargetSpec {
	spec.TripleAliases = slices.Clone(spec.TripleAliases)
	return spec
}

// ResolveTarget returns the declared TargetSpec for an explicitly supported
// (goos, goarch) pair, independent of the running host. Unknown operating
// systems or architectures are rejected explicitly.
func ResolveTarget(goos, goarch string) (TargetSpec, error) {
	osName := strings.TrimSpace(goos)
	archName := strings.TrimSpace(goarch)
	for _, spec := range shippingTargets {
		if spec.GOOS == osName && spec.GOARCH == archName {
			return cloneTargetSpec(spec), nil
		}
	}
	return TargetSpec{}, fmt.Errorf("unsupported portable python native engine platform %q", osName+"/"+archName)
}

// ResolveTargetTriple maps a packaging target triple or alias (such as
// "aarch64-apple-darwin" or "x86_64-unknown-linux-gnu") to its declared
// TargetSpec, independent of the running host. Unknown triples are rejected
// explicitly.
func ResolveTargetTriple(triple string) (TargetSpec, error) {
	trimmed := strings.TrimSpace(triple)
	for _, spec := range shippingTargets {
		if slices.Contains(spec.TripleAliases, trimmed) {
			return cloneTargetSpec(spec), nil
		}
	}
	return TargetSpec{}, fmt.Errorf("unsupported portable python native engine target %q", triple)
}
