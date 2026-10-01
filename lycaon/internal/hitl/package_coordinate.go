package hitl

import (
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/packageexec"
)

// PackageCoordinatePattern formats package execution metadata into a deterministic
// pattern string matching the package coordinates without command line flags or env vars.
func PackageCoordinatePattern(exec *packageexec.Execution) string {
	if exec == nil || len(exec.Packages) == 0 {
		return ""
	}
	manager := strings.TrimSpace(exec.Manager)
	op := string(exec.Operation)
	parts := make([]string, 0, len(exec.Packages))
	for _, pkg := range exec.Packages {
		version := strings.TrimSpace(pkg.ResolvedVersion)
		if version == "" {
			version = strings.TrimSpace(pkg.RequestedVersion)
		}
		system := strings.TrimSpace(pkg.System)
		name := strings.TrimSpace(pkg.Name)
		nonce := strings.TrimSpace(pkg.ResolutionNonce)
		coord := manager + ":" + op + ":" + system + ":" + name + ":" + version
		if pkg.Status != packageexec.IdentityResolved && nonce != "" {
			coord += ":" + nonce
		}
		parts = append(parts, coord)
	}
	sort.Strings(parts)
	return strings.Join(parts, "\n")
}
