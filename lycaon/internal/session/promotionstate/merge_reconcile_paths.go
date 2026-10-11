package promotionstate

import (
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/sandbox"
)

// SetMergeReconcilePaths registers editable promotion paths.
func (m *Service) SetMergeReconcilePaths(sessionID string, paths []string) {
	if m == nil {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return
	}
	normalized := normalizeMergeReconcilePaths(paths)
	if len(normalized) == 0 {
		m.mergeReconcile.Delete(sessionID)
		return
	}
	set := make(map[string]struct{}, len(normalized))
	for _, p := range normalized {
		set[p] = struct{}{}
	}
	m.mergeReconcile.Store(sessionID, set)
}

// ClearMergeReconcilePaths drops reconcile edit allowances for a session.
func (m *Service) ClearMergeReconcilePaths(sessionID string) {
	if m == nil {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return
	}
	m.mergeReconcile.Delete(sessionID)
}

// Allowed reports whether relPath is in the active merge-reconcile set for sessionID.
func (m *Service) Allowed(sessionID, relPath string) bool {
	if m == nil {
		return false
	}

	m.mu.Lock()
	defer m.mu.Unlock()
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
