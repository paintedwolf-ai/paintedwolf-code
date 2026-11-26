package scan

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/confine"
)

// BundledScannerConfinement permits project reads and output writes without egress.
func BundledScannerConfinement(projectDir string, workspace confine.ActionWorkspace) (*confine.Confinement, error) {
	if strings.TrimSpace(projectDir) == "" {
		return nil, fmt.Errorf("bundled scanner project root is required")
	}
	if workspace.Path() == "" {
		return nil, fmt.Errorf("bundled scanner output root is required")
	}
	cache, err := confine.NewScannerCache()
	if err != nil {
		return nil, fmt.Errorf("bundled scanner cache: %w", err)
	}
	box, applied := confine.DefaultConfinement(confine.Request{
		ActionWorkspace: workspace, ScannerCache: cache, ReadRoots: []string{projectDir}, Egress: confine.EgressDeny,
	})
	if err := confine.RequireApplied(applied); err != nil {
		return nil, err
	}
	return box, nil
}
