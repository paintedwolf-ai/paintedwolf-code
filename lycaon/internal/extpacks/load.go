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

// ArchiveKindRoot holds sealed copies of released workflow versions:
// archive/<workflow>/<version>/ with workflow.yaml, guidance/<stem>.md, and
// guidance/gate-feedback/<stem>.yaml.
const ArchiveKindRoot = "archive"

// ArchiveKey names one sealed workflow version.
func ArchiveKey(workflowID, version string) string {
	return strings.ToLower(strings.TrimSpace(workflowID)) + "/" + strings.TrimSpace(version)
}

// ArchiveWorkflowUnitID returns the unit id of a sealed manifest.
func ArchiveWorkflowUnitID(key string) string {
	return ArchiveKindRoot + "/" + key + "/workflow"
}

// ArchiveGuidanceUnitID returns the unit id of sealed guidance, where stem is
// relative to guidance/ (gate-feedback/<id> for gate feedback).
func ArchiveGuidanceUnitID(key, stem string) string {
	return ArchiveKindRoot + "/" + key + "/" + GuidanceUnitID(stem)
}

// SplitArchiveUnitID returns the archive key and the unit's path inside it.
func SplitArchiveUnitID(unitID string) (key, rest string, ok bool) {
	parts := strings.SplitN(unitID, "/", 4)
	if len(parts) != 4 || parts[0] != ArchiveKindRoot || parts[1] == "" || parts[2] == "" {
		return "", "", false
	}
	return parts[1] + "/" + parts[2], parts[3], true
}

// archiveUnitID maps a pack-relative archive path to its unit id.
func archiveUnitID(parts []string) string {
	if len(parts) < 4 || parts[1] == "" || parts[2] == "" {
		return ""
	}
	key := ArchiveKey(parts[1], parts[2])
	switch rest := parts[3:]; {
	case len(rest) == 1 && rest[0] == "workflow.yaml":
		return ArchiveWorkflowUnitID(key)
	case len(rest) == 2 && rest[0] == "guidance" && strings.HasSuffix(rest[1], ".md"):
		return ArchiveGuidanceUnitID(key, strings.TrimSuffix(rest[1], ".md"))
	case len(rest) == 3 && rest[0] == "guidance" && rest[1] == GuidanceGateFeedbackDir && strings.HasSuffix(rest[2], ".yaml"):
		return ArchiveGuidanceUnitID(key, GuidanceGateFeedbackDir+"/"+strings.TrimSuffix(rest[2], ".yaml"))
	default:
		return ""
	}
}

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
