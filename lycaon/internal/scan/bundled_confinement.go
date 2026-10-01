package scan

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/confine"
)

// BundledScannerConfinement permits project reads and output writes without egress.
func BundledScannerConfinement(projectDir, outputDir string) (*confine.Confinement, error) {
	if strings.TrimSpace(projectDir) == "" {
		return nil, fmt.Errorf("bundled scanner project root is required")
	}
	if strings.TrimSpace(outputDir) == "" {
		return nil, fmt.Errorf("bundled scanner output root is required")
	}
	box, applied := confine.DefaultConfinement(confine.Request{
		Roots: []string{outputDir}, ReadRoots: []string{projectDir}, Egress: confine.EgressDeny,
	})
	if err := confine.RequireApplied(applied); err != nil {
		return nil, err
	}
	return box, nil
}
