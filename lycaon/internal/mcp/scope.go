package mcp

import (
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/tools"
)

// deviceScopeKey is the session-map key for host-initiated inspection that names no
// project. It cannot collide with a project id: ids are UUIDs.
const deviceScopeKey = "\x00device"

// CallScope binds an MCP operation to one project and its confinement roots.
// A zero value represents device scope.
type CallScope struct {
	// ProjectID is the registry id of the project this operation serves.
	ProjectID string
	// ProjectDir is the primary root containing the project overlay.
	ProjectDir string
	// Roots advertise the MCP roots capability and confine local stdio processes.
	Roots []string
}

// ScopeFromToolContext builds the scope for the invoking tool session.
func ScopeFromToolContext(tctx tools.ToolContext) CallScope {
	scope := CallScope{ProjectID: strings.TrimSpace(tctx.ProjectID)}
	for _, root := range tctx.Roots {
		p := strings.TrimSpace(root.Path)
		if p == "" {
			continue
		}
		abs := filepath.Clean(p)
		scope.Roots = append(scope.Roots, abs)
		if root.IsPrimary && scope.ProjectDir == "" {
			scope.ProjectDir = abs
		}
	}
	if scope.ProjectDir == "" && len(scope.Roots) > 0 {
		scope.ProjectDir = scope.Roots[0]
	}
	return scope
}

// ProjectScope builds the scope for a host request that names a project.
func ProjectScope(projectID, projectDir string, roots []string) CallScope {
	return CallScope{
		ProjectID:  strings.TrimSpace(projectID),
		ProjectDir: strings.TrimSpace(projectDir),
		Roots:      cleanRootPaths(roots),
	}
}

// sessionKey isolates provider sessions by project.
func (s CallScope) sessionKey() string {
	if id := strings.TrimSpace(s.ProjectID); id != "" {
		return id
	}
	if dir := strings.TrimSpace(s.ProjectDir); dir != "" {
		return dir
	}
	return deviceScopeKey
}

// isDevice reports the scope that names no project.
func (s CallScope) isDevice() bool {
	return s.sessionKey() == deviceScopeKey
}
