package scan

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/confine"
)

// BundledScannerConfinement permits project reads (plus any read-only engine
// runtime roots) and ActionWorkspace writes without egress.
func BundledScannerConfinement(projectDir string, workspace confine.ActionWorkspace, extraReadRoots ...string) (*confine.Confinement, error) {
	if strings.TrimSpace(projectDir) == "" {
		return nil, fmt.Errorf("bundled scanner project root is required")
	}
	if workspace.Path() == "" {
		return nil, fmt.Errorf("bundled scanner output root is required")
	}
	readRoots := make([]string, 0, 1+len(extraReadRoots))
	readRoots = append(readRoots, projectDir)
	for _, root := range extraReadRoots {
		if trimmed := strings.TrimSpace(root); trimmed != "" {
			readRoots = append(readRoots, trimmed)
		}
	}
	box, applied := confine.DefaultConfinement(confine.Request{
		ActionWorkspace: workspace,
		ReadRoots:       readRoots,
		Egress:          confine.EgressDeny,
	})
	if err := confine.RequireApplied(applied); err != nil {
		return nil, err
	}
	return box, nil
}
