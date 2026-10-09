package tools

import (
	"github.com/lycaon/lycaon/internal/projectroot"
	"path/filepath"

	"strings"
)

func SocketArgPath(tc ToolContext, raw string) string {
	raw = strings.TrimSpace(raw)
	if abs, err := ApprovalFilePath(tc, raw); err == nil {
		return abs
	}
	return raw
}

func ApprovalFilePath(tc ToolContext, path string) (string, error) {
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
