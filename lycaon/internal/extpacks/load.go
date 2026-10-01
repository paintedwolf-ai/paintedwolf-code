package extpacks

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/settingsoverlay"
)

// ResolveCatalog builds a catalog for an ordered project overlay.
func ResolveCatalog(ctx context.Context, projectDirs []string, scanners ScannerRequirementChecker) (*EffectiveCatalog, error) {
	if err := settingsoverlay.CheckFormats(projectDirs); err != nil {
		return nil, err
	}
	projectDir := firstProjectDir(projectDirs)
	release, err := AcquireIntentLocks(projectDirs)
	if err != nil {
		return nil, err
	}
	defer release()
	content, err := discoverAllContent(projectDirs)
	if err != nil {
		return nil, err
	}
	desired, provenance, err := LoadMergedDesired(projectDirs)
	if err != nil {
		return nil, err
	}
	eff := Resolve(ctx, ResolveInput{
		Packs:      content,
		Desired:    desired,
		ProjectID:  strings.TrimSpace(projectDir),
		Scanners:   scanners,
		Provenance: provenance,
	})
	if extra := SuggestionDiagnostics(projectDirs); len(extra) > 0 {
		eff.Diagnostics = StampSeverities(append(eff.Diagnostics, extra...))
	}
	return eff, nil
}

// ResolveStockCatalog builds the bundled catalog without device state.
func ResolveStockCatalog(ctx context.Context, scanners ScannerRequirementChecker) (*EffectiveCatalog, error) {
	content, err := DiscoverStockContent()
	if err != nil {
		return nil, err
	}
	return Resolve(ctx, ResolveInput{
		Packs:    content,
		Desired:  EmptyDesired(),
		Scanners: scanners,
	}), nil
}

func firstProjectDir(projectDirs []string) string {
	for _, projectDir := range projectDirs {
		if projectDir = strings.TrimSpace(projectDir); projectDir != "" {
			return projectDir
		}
	}
	return ""
}

// PolicyUnitID returns the public unit id for a qualified policy identity.
func PolicyUnitID(code string) string {
	return "policy/" + code
}

// GuidanceUnitID returns the public unit id for a guidance stem (no .md).
func GuidanceUnitID(stem string) string {
	return "guidance/" + stem
}

// WorkflowUnitIDPrefix includes manifests, templates, and topologies.
const WorkflowUnitIDPrefix = "workflows/"

// WorkflowUnitID returns the public unit id for a workflow id.
func WorkflowUnitID(id string) string {
	return WorkflowUnitIDPrefix + id
}

// MCPBindingUnitID returns the public unit id for an mcp_bindings document stem.
func MCPBindingUnitID(id string) string {
	return "mcp_bindings/" + id
}

// TopologyUnitID returns the public unit id for a topology id.
func TopologyUnitID(id string) string {
	return "workflows/_topologies/" + id
}

// WorkflowTemplateUnitIDPrefix namespaces the workflow template provide units.
const WorkflowTemplateUnitIDPrefix = WorkflowUnitIDPrefix + "_templates/"

// StockFloorCatalog resolves stock content and records the rejected state.
func StockFloorCatalog(ctx context.Context, scanners ScannerRequirementChecker, cause error) (*EffectiveCatalog, error) {
	eff, err := ResolveStockCatalog(ctx, scanners)
	if err != nil {
		return nil, err
	}
	eff.Diagnostics = append(eff.Diagnostics, Diagnostic{
		Code: DiagDesiredStateRejected,
		Message: fmt.Sprintf(
			"committed extension state did not produce a bootable catalog; serving the stock floor until it is repaired: %v", cause),
	})
	return eff, nil
}

// ApplyCatalog installs a bootable device catalog.
func ApplyCatalog(ctx context.Context, projectDirs []string, scanners ScannerRequirementChecker) (*EffectiveCatalog, error) {
	stamp := DeviceDesiredStamp()
	eff, err := ResolveCatalog(ctx, projectDirs, scanners)
	if err != nil {
		return nil, err
	}
	if err := eff.BootError(); err != nil {
		markActiveStamp(stamp)
		return eff, err
	}
	if _, _, err := LoadEffectiveApprovalRules(eff); err != nil {
		markActiveStamp(stamp)
		return eff, err
	}
	setActiveWithStamp(eff, stamp)
	return eff, nil
}
