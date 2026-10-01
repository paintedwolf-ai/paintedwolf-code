package session

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/pkg/api"
)

// MergeReconcileRegistrar records merge state and editable conflict paths.
type MergeReconcileRegistrar interface {
	SetMergeReconcilePaths(sessionID string, paths []string)
	ClearMergeReconcilePaths(sessionID string)
	RecordPromotePathStatus(sessionID, jobID string, statuses []api.WorkerPromotePathStatus)
	RecordOverlayPreviewSummary(sessionID, jobID string, out *api.WorkerMergeResult)
	ClearPromotePathStatus(sessionID, jobID string)
	// RecordPromotedPrimaryPaths captures rewind pre-images.
	RecordPromotedPrimaryPaths(ctx context.Context, sessionID string, paths []string)
}

// SetMergeReconcilePaths registers editable promotion paths.
func (m *Manager) SetMergeReconcilePaths(sessionID string, paths []string) {
	if m == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return
	}
	normalized := normalizeMergeReconcilePaths(paths)
	if len(normalized) == 0 {
		m.ClearMergeReconcilePaths(sessionID)
		return
	}
	set := make(map[string]struct{}, len(normalized))
	for _, p := range normalized {
		set[p] = struct{}{}
	}
	m.mergeReconcile.Store(sessionID, set)
}

// ClearMergeReconcilePaths drops reconcile edit allowances for a session.
func (m *Manager) ClearMergeReconcilePaths(sessionID string) {
	if m == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return
	}
	m.mergeReconcile.Delete(sessionID)
}

// Allowed reports whether relPath is in the active merge-reconcile set for sessionID.
func (m *Manager) Allowed(sessionID, relPath string) bool {
	if m == nil {
		return false
	}
	sessionID = strings.TrimSpace(sessionID)
	relPath = filepath.ToSlash(filepath.Clean(strings.TrimSpace(relPath)))
	if sessionID == "" || relPath == "" || relPath == "." || sandbox.HasParentTraversal(relPath) {
		return false
	}
	set, ok := m.mergeReconcile.Load(sessionID)
	if !ok {
		return false
	}
	_, ok = set[relPath]
	return ok
}

func normalizeMergeReconcilePaths(paths []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, raw := range paths {
		p := filepath.ToSlash(filepath.Clean(strings.TrimSpace(raw)))
		if p == "" || p == "." || sandbox.HasParentTraversal(p) {
			continue
		}
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	return out
}
