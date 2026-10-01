package scan

import (
	"context"
	"strings"

	scancatalog "github.com/lycaon/lycaon/internal/scan/catalog"
)

// RequirementChecker reports scanner availability for a catalog scope.
type RequirementChecker struct {
	ModuleRoot string
	HomeDir    string
}

// ScannerEnabled receives the absolute project directory as projectID.
func (c RequirementChecker) ScannerEnabled(_ context.Context, projectID, scannerID string) bool {
	scannerID = strings.TrimSpace(scannerID)
	if scannerID == "" || strings.TrimSpace(c.ModuleRoot) == "" {
		return false
	}
	cfg, err := scancatalog.LoadMergedScannerConfig(c.ModuleRoot, strings.TrimSpace(projectID), c.HomeDir)
	if err != nil || cfg == nil {
		return false
	}
	for _, e := range cfg.Scanners {
		if e.ID != scannerID {
			continue
		}
		return scannerRunnable(e)
	}
	return false
}

func scannerRunnable(e scancatalog.ScannerEntry) bool {
	if !e.EnabledOrDefault() {
		return false
	}
	if strings.TrimSpace(e.Driver) != scancatalog.DriverExternal {
		return true
	}
	return scancatalog.BinaryOnPath(e.Command)
}
