package scan

import (
	"fmt"
	"strings"

	scancatalog "github.com/lycaon/lycaon/internal/scan/catalog"
)

// scopePreviewNames limits ecosystem names in the scope note.
const scopePreviewNames = 4

// ScanScopeFor returns the declared coverage note.
// sastFloor applies only to source_host_floor contracts.
func ScanScopeFor(contract scancatalog.ScannerContract, sastFloor SASTFloor) string {
	switch contract.Scope {
	case scancatalog.ScopeSourceHostFloor:
		return sastFloor.note()
	case scancatalog.ScopeSourceDriver:
		return "source paths selected by the scanner driver"
	case scancatalog.ScopeDependencies:
		return "lockfiles + installed deps under project root; gitignore-aware"
	case scancatalog.ScopeSecrets:
		return "tracked paths under project root"
	case scancatalog.ScopeContainer:
		return "container images and filesystem targets configured for the scan"
	default:
		return "project paths selected by the scanner driver"
	}
}

// SASTFloor carries path exclusions into the scope note.
type SASTFloor struct {
	Leads    []string
	Patterns []string
}

func (f SASTFloor) note() string {
	if len(f.Patterns) == 0 {
		return "source; engine default path filters"
	}
	preview := f.Leads
	if len(preview) == 0 {
		preview = f.Patterns
	}
	if len(preview) > scopePreviewNames {
		preview = preview[:scopePreviewNames]
	}
	return fmt.Sprintf(
		"source; excludes %d dependency and build paths from scanners/scan-excludes.yaml (%s, …)",
		len(f.Patterns), strings.Join(preview, ", "),
	)
}
