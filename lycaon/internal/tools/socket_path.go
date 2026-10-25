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
	if area, rel, ok := projectroot.SessionAddress(path); ok {
		dir := tc.Host.SessionScratchDir
		unavailable := ErrSessionScratchUnavailable
		if area == projectroot.VirtualOutputLabel {
			dir = tc.Host.SessionOutputDir
			unavailable = ErrSessionOutputUnavailable
		}
		if strings.TrimSpace(dir) == "" {
			return "", unavailable
		}
		return projectroot.SessionPath(dir, rel)
	}

	roots, active := tc.Source.Roots, tc.Source.ActiveRootID
	if branch := strings.TrimSpace(tc.Source.WorkerBranchRoot); branch != "" {
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
