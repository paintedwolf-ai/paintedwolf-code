package tools

import (
	"errors"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/pkg/pathglob"
)

// CommandSurfaceObservation maps parse failures to COMMAND_NOT_ARGV.
// Recovery follows the invocation mode as well as the accepted schema fields.
func CommandSurfaceObservation(
	toolName, profileID, displayCommand string,
	args map[string]any,
	argFields []string,
	err error,
) *ToolReject {
	if err == nil || !commandsurface.IsCommandSurfaceError(err) {
		return nil
	}
	switch toolName {
	case "command", "verify", "terminal_open":
	default:
		return nil
	}
	rawCommand, _ := args["command"].(string)
	vars := commandsurface.CommandRejectVars(profileID, displayCommand, rawCommand, argFields, err)
	_, capture := args["terminal_capture"].(map[string]any)
	vars["terminal_capture"] = toolName == "command" && capture
	return RejectInvalidArguments("COMMAND_NOT_ARGV", vars)
}

// ScopeObservation maps coordinator sandbox scope failures to observation tokens.
func ScopeObservation(profileID, turnSurfaceID, toolName, path string, err error) *ToolReject {
	if err == nil || profileID != coordinatorProfileID {
		return nil
	}
	var scopeErr *sandbox.ScopeError
	if !errors.As(err, &scopeErr) {
		return nil
	}
	code := coordinatorScopeRejectCode(toolName, turnSurfaceID, scopeErr.Kind)
	if code == "" {
		return nil
	}
	if path == "" {
		path = scopeErr.Path
	}
	data := map[string]any{
		"tool": toolName,
		"path": path,
	}
	if code == "COORDINATOR_INVESTIGATE_DENIED_PATH" {
		for k, v := range coordinatorInvestigateDenyData(path) {
			data[k] = v
		}
	}
	return &ToolReject{Code: code, Data: data}
}

func coordinatorScopeRejectCode(toolName, turnSurfaceID string, kind sandbox.ScopeKind) string {
	switch kind {
	case sandbox.ScopeWrite:
		if !toolcontract.MutatesContent(toolName) {
			return ""
		}
		if turnSurfaceID == SurfaceImplementInvestigate {
			return "COORDINATOR_INVESTIGATE_DENIED_PATH"
		}
		return ""
	case sandbox.ScopeRead:
		switch toolName {
		case "read", "grep":
			return "COORDINATOR_READ_OUTSIDE_SCOPE"
		}
	}
	return ""
}

func coordinatorInvestigateDenyData(path string) map[string]any {
	path = filepath.ToSlash(strings.TrimSpace(path))
	switch {
	case pathglob.Match(".git/**", path):
		return map[string]any{
			"escape":     "Use command for git commands",
			"path_class": "vcs",
		}
	case pathglob.Match("node_modules/**", path), pathglob.Match("vendor/**", path):
		return map[string]any{
			"escape":     "Run the package manager via command",
			"path_class": "dependencies",
		}
	default:
		return map[string]any{
			"escape":     "Use the tool that owns this path class",
			"path_class": "other",
		}
	}
}

func pathFromToolArgs(args map[string]any) string {
	if p, ok := args["path"].(string); ok && p != "" {
		return p
	}
	return ""
}
