package tools

import (
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/projectroot"
)

// Unresolved entries remain empty so partial resolution cannot widen a grant.
func resolvedApprovalFiles(tool string, args map[string]any, tc ToolContext) []string {
	files := filesFromArgs(tool, args)
	if len(files) == 0 {
		return nil
	}
	resolved := make([]string, len(files))
	for i, path := range files {
		abs, err := approvalFilePath(tc, path)
		if err == nil {
			resolved[i] = fspath.CanonicalPath(abs)
		}
	}
	return resolved
}

func approvalFilePath(tc ToolContext, path string) (string, error) {
	if filepath.IsAbs(path) {
		return path, nil
	}
	if rel, ok := projectroot.ScratchAddress(path); ok {
		if strings.TrimSpace(tc.SessionScratchDir) == "" {
			return "", ErrSessionScratchUnavailable
		}
		return projectroot.ScratchPath(tc.SessionScratchDir, rel)
	}
	roots, active := tc.Roots, tc.ActiveRootID
	if branch := strings.TrimSpace(tc.WorkerBranchRoot); branch != "" {
		rel, _, err := projectroot.WorkerBranchRelative(roots, active, path)
		if err != nil {
			return "", err
		}
		path = rel
		roots = []projectroot.RootRef{{ID: "worker-branch", Path: branch, IsPrimary: true}}
		active = "worker-branch"
	}
	abs, _, err := projectroot.ResolveAbs(roots, active, path)
	return abs, err
}
