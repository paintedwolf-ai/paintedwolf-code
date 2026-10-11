package settings

import (
	"path/filepath"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/egressproxy"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/hostscope"
)

// pathScopedSet comes from the native tool catalog.
var pathScopedSet = tierSet(pathScopedTools)

// commandScopedSet comes from the native tool catalog.
var commandScopedSet = tierSet(commandScopedTools)

var processSpawningSet = tierSet(processSpawningTools)

// GrantPredicateForAction derives a predicate from structured action data.
func GrantPredicateForAction(action hitl.ProposedAction) ApprovalRule {
	tool := strings.TrimSpace(action.Invocation.Tool)
	switch {
	case action.Resources.ApprovalCategory == string(ApprovalCategoryMCP) && strings.TrimSpace(action.Resources.ApprovalSubject) != "":
		return ApprovalRule{Category: ApprovalCategoryMCP, Pattern: strings.TrimSpace(action.Resources.ApprovalSubject)}
	case tool == "write_root":
		root, _ := action.Invocation.Args["proposed_write_root"].(string)
		return ApprovalRule{Category: ApprovalCategoryWriteRoot, Pattern: strings.TrimSpace(root)}
	case tool == "network":
		host, _ := action.Invocation.Args["host"].(string)
		if strings.TrimSpace(host) == "" {
			// A declared set has no single host to widen from.
			return ApprovalRule{Category: ApprovalCategoryHost}
		}
		// An opaque tunnel leases the registrable site bound to its port.
		if opaqueEgressAction(action.Invocation.Args) {
			return ApprovalRule{Category: ApprovalCategoryHost, Pattern: hostscope.TunnelPattern(host, egressActionPort(action.Invocation.Args))}
		}
		return ApprovalRule{Category: ApprovalCategoryHost, Pattern: hostscope.Pattern(host)}
	case IsCommandToolName(tool):
		return ApprovalRule{Category: ApprovalCategoryCommand, Pattern: CommandTextFromActionArgs(action.Invocation.Args)}
	case isPathScopedTool(tool):
		files := action.Invocation.Files
		if action.Invocation.ResolvedFiles != nil {
			if len(action.Invocation.ResolvedFiles) != len(files) {
				return ApprovalRule{Category: ApprovalCategoryPath}
			}
			files = action.Invocation.ResolvedFiles
			for _, file := range files {
				if !filepath.IsAbs(file) {
					return ApprovalRule{Category: ApprovalCategoryPath}
				}
			}
		}
		return ApprovalRule{Category: ApprovalCategoryPath, Pattern: exactPathScope(files)}
	default:
		return ApprovalRule{Category: ApprovalCategoryTool, Pattern: tool}
	}
}

// opaqueEgressAction reads the broker-stamped transport.
func opaqueEgressAction(args map[string]any) bool {
	transport, _ := args["transport"].(string)
	return egressproxy.Transport(strings.TrimSpace(transport)).Opaque()
}

// JSON decoding represents the broker-stamped port as float64.
func egressActionPort(args map[string]any) uint16 {
	switch v := args["port"].(type) {
	case uint16:
		return v
	case int:
		if v > 0 && v <= 65535 {
			return uint16(v)
		}
	case float64:
		if v > 0 && v <= 65535 {
			return uint16(v)
		}
	}
	return 0
}

func hostResourceGrantPredicate(action hitl.ProposedAction) ApprovalRule {
	ids := append([]string(nil), action.Resources.HostResources...)
	sort.Strings(ids)
	return ApprovalRule{Category: ApprovalCategoryHostResource, Pattern: strings.Join(ids, ",")}
}

func isPathScopedTool(tool string) bool {
	_, ok := pathScopedSet[strings.TrimSpace(strings.ToLower(tool))]
	return ok
}

// IsCommandToolName reports whether tool is a command surface.
func IsCommandToolName(tool string) bool {
	_, ok := commandScopedSet[strings.TrimSpace(strings.ToLower(tool))]
	return ok
}

func isProcessSpawningTool(tool string) bool {
	_, ok := processSpawningSet[strings.TrimSpace(strings.ToLower(tool))]
	return ok
}

func exactPathScope(files []string) string {
	if len(files) == 0 {
		return ""
	}
	if len(files) == 1 {
		return filepath.ToSlash(strings.TrimSpace(files[0]))
	}
	// Multi-file actions remain exact-action approvals.
	return ""
}
