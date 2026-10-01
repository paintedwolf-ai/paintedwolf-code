package settings

import (
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/ingestion"
)

// ReversibilityTier classifies how hard an action is to undo.
type ReversibilityTier int

const (
	TierReversible ReversibilityTier = iota
	TierRecoverable
	TierIrreversible
)

// Tool tiers come from native-tools.yaml.

func tierSet(tools []string) map[string]struct{} {
	out := make(map[string]struct{}, len(tools))
	for _, t := range tools {
		out[t] = struct{}{}
	}
	return out
}

var (
	reversibleSet   = tierSet(reversibleTools)
	recoverableSet  = tierSet(recoverableTools)
	irreversibleSet = tierSet(irreversibleTools)
)

// ClassifyTier maps a proposed action to a reversibility tier.
func ClassifyTier(action hitl.ProposedAction) ReversibilityTier {
	// Writes outside the execution roots are irreversible.
	if PathEscapesWorkspace(action) {
		return TierIrreversible
	}

	tool := strings.TrimSpace(action.Tool)

	// Commands require both confinement boundaries to be recoverable.
	if IsCommandToolName(tool) {
		if boundaryHolds(action.Contained) {
			return TierRecoverable
		}
		return TierIrreversible
	}

	if base, ok := baseToolTier(tool); ok {
		return base
	}
	return TierIrreversible
}

// TierLabel returns the wire/detail_json tier vocabulary for a reversibility tier.
func TierLabel(tier ReversibilityTier) string {
	switch tier {
	case TierReversible:
		return "reversible"
	case TierRecoverable:
		return "recoverable"
	default:
		return "irreversible"
	}
}

func baseToolTier(tool string) (ReversibilityTier, bool) {
	if _, ok := irreversibleSet[tool]; ok {
		return TierIrreversible, true
	}
	if _, ok := recoverableSet[tool]; ok {
		return TierRecoverable, true
	}
	if _, ok := reversibleSet[tool]; ok {
		return TierReversible, true
	}
	if ingestion.IsMCPToolName(tool) {
		return TierRecoverable, true
	}
	return 0, false
}

// workspaceRoots combines confinement and project roots for escape checks.
func workspaceRoots(action hitl.ProposedAction) []string {
	roots := make([]string, 0, len(action.Contained.Roots)+1)
	for _, raw := range action.Contained.Roots {
		if root := normalizeApprovalPath(strings.TrimSpace(raw)); root != "" {
			roots = append(roots, root)
		}
	}
	if root := normalizeApprovalPath(strings.TrimSpace(action.ProjectDir)); root != "" {
		roots = append(roots, root)
	}
	return roots
}

// PathEscapesWorkspace reports whether any file path this action names falls outside the
// roots it runs under.
func PathEscapesWorkspace(action hitl.ProposedAction) bool {
	files := action.Files
	if len(files) == 0 {
		return false
	}
	roots := workspaceRoots(action)
	if len(roots) == 0 {
		for _, f := range files {
			if strings.TrimSpace(f) != "" {
				return true
			}
		}
		return false
	}
	for _, raw := range files {
		if PathEscapesRoots(roots, raw) {
			return true
		}
	}
	return false
}

// PathEscapesRoots reports whether one path leaves every execution root.
func PathEscapesRoots(roots []string, raw string) bool {
	file := normalizeApprovalPath(strings.TrimSpace(raw))
	if file == "" {
		return false
	}
	if rootsContain(roots, file) {
		return false
	}
	if !strings.HasPrefix(file, "/") && !isWindowsAbsPath(file) {
		return relPathEscapes(file)
	}
	return true
}

func rootsContain(roots []string, file string) bool {
	// Compare lexical and canonical spellings.
	canonicalFile := canonicalApprovalPath(file)
	for _, root := range roots {
		if pathUnder(file, root) || pathUnder(canonicalFile, canonicalApprovalPath(root)) {
			return true
		}
	}
	return false
}

func pathUnder(file, root string) bool {
	if file == "" || root == "" {
		return false
	}
	return file == root || strings.HasPrefix(file, root+"/")
}

// canonicalApprovalPath resolves aliases for absolute paths.
func canonicalApprovalPath(path string) string {
	if !strings.HasPrefix(path, "/") && !isWindowsAbsPath(path) {
		return path
	}
	return normalizeApprovalPath(fspath.CanonicalPath(path))
}

// relPathEscapes cleans traversal before checking the workspace boundary.
func relPathEscapes(rel string) bool {
	cleaned := filepath.ToSlash(filepath.Clean(rel))
	return cleaned == ".." || strings.HasPrefix(cleaned, "../")
}

func normalizeApprovalPath(path string) string {
	path = filepath.ToSlash(strings.TrimSpace(path))
	return strings.TrimSuffix(path, "/")
}

func isWindowsAbsPath(path string) bool {
	if len(path) < 2 {
		return false
	}
	return path[1] == ':'
}

// ApprovalRecoverableToolIDs returns recoverable-tier tool names (from catalog codegen).
func ApprovalRecoverableToolIDs() []string {
	return append([]string(nil), recoverableTools...)
}

// ToolTierDeclared reports whether a tool has a defined tier.
func ToolTierDeclared(tool string) bool {
	tool = strings.TrimSpace(tool)
	if _, ok := reversibleSet[tool]; ok {
		return true
	}
	if _, ok := recoverableSet[tool]; ok {
		return true
	}
	if _, ok := irreversibleSet[tool]; ok {
		return true
	}
	return ingestion.IsMCPToolName(tool)
}

// ApprovalIrreversibleToolIDs returns irreversible-tier tool names (from catalog codegen).
func ApprovalIrreversibleToolIDs() []string {
	return append([]string(nil), irreversibleTools...)
}

// boundaryHolds reports whether filesystem and egress confinement both apply.
func boundaryHolds(c hitl.Contained) bool {
	return c.FSJailed &&
		(c.Egress == hitl.ContainedEgressDeny || c.Egress == hitl.ContainedEgressProxy)
}
