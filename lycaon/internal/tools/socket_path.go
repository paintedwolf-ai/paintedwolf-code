package tools

import (
	"path/filepath"

	"github.com/lycaon/lycaon/internal/projectroot"

	"strings"
)

func SocketArgPath(tc ToolContext, raw string) string {
	raw = strings.TrimSpace(raw)
	if filepath.IsAbs(raw) {
		return raw
	}
	if abs, err := ApprovalFilePath(tc, raw); err == nil {
		return abs
	}
	return raw
}

func ApprovalFilePath(tc ToolContext, path string) (string, error) {
	if filepath.IsAbs(path) {
		if backing, ok := PrivateTempOperand(tc, path); ok {
			return backing, nil
		}
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
		roots = []projectroot.RootRef{{ID: projectroot.WorkerBranchRootID, Path: branch, IsPrimary: true}}
		active = projectroot.WorkerBranchRootID
	}
	abs, _, err := projectroot.ResolveAbs(roots, active, path)
	return abs, err
}
