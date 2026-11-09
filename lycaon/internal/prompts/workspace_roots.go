package prompts

import (
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/pongoplain"
	"github.com/lycaon/lycaon/internal/projectroot"
)

// WorkspaceRootRows builds the prompt rows shared by live assembly and
// prompt_dump: one row per root with label, path, is_primary, and is_active
// matched by the active root ID, capped at the prompt collection limit. The
// second return is the number of rows omitted by the cap.
func WorkspaceRootRows(roots []projectroot.RootRef, activeRootID string) ([]map[string]any, int) {
	active, _ := activeWorkspaceRoot(roots, activeRootID)
	limit := len(roots)
	if limit > pongoplain.MaxCollectionItems {
		limit = pongoplain.MaxCollectionItems
	}
	rows := make([]map[string]any, 0, limit)
	for _, r := range roots[:limit] {
		rows = append(rows, map[string]any{
			"label":      r.Label,
			"path":       r.Path,
			"is_primary": r.IsPrimary,
			"is_active":  active.ID != "" && r.ID == active.ID,
		})
	}
	return rows, len(roots) - len(rows)
}

// activeWorkspaceRoot resolves the session's active root by ID, falling back
// to the primary root when the ID is empty or unknown.
func activeWorkspaceRoot(roots []projectroot.RootRef, activeRootID string) (projectroot.RootRef, bool) {
	if r, err := projectroot.ActiveRoot(roots, activeRootID); err == nil {
		return r, true
	}
	if r, err := projectroot.PrimaryRoot(roots); err == nil {
		return r, true
	}
	return projectroot.RootRef{}, false
}

// MergeWorkspaceRootsVars adds workspace_roots, root_count, project_dir, and
// workspace_roots_omitted_count for workspace-roots.md.
func MergeWorkspaceRootsVars(vars map[string]any, roots []projectroot.RootRef, activeRootID string) {
	if vars == nil {
		return
	}
	if _, ok := vars["reviews_confined_writes"].(bool); !ok {
		vars["reviews_confined_writes"] = true
	}
	vars["root_count"] = len(roots)
	rows, omitted := WorkspaceRootRows(roots, activeRootID)
	vars["workspace_roots"] = rows
	vars["workspace_roots_omitted_count"] = omitted
	if active, ok := activeWorkspaceRoot(roots, activeRootID); ok && active.Path != "" {
		vars["project_dir"] = active.Path
	}
}

// RootsChangedKickData builds the root-change notice context: one row per
// added, removed, moved or renamed root in the change, then the root set the
// next turn resolves against.
func RootsChangedKickData(change projectroot.RootChange, after []projectroot.RootRef, activeRootID string) map[string]any {
	changes := make([]map[string]any, 0, len(change.Added)+len(change.Retired))
	for _, r := range change.Added {
		changes = append(changes, map[string]any{"kind": "added", "label": r.Label})
	}
	for _, r := range change.Retired {
		row := map[string]any{"kind": string(r.Reason), "label": r.Root.Label, "old_label": r.Root.Label}
		if r.Reason != projectroot.RootDetached {
			row["label"] = r.Successor.Label
		}
		changes = append(changes, row)
	}
	if len(changes) > pongoplain.MaxCollectionItems {
		changes = changes[:pongoplain.MaxCollectionItems]
	}
	activeID := ""
	data := map[string]any{
		"changes":             changes,
		"changes_omitted":     len(change.Added) + len(change.Retired) - len(changes),
		"generation":          change.Generation,
		"primary_changed":     change.Primary.ID != "",
		"new_primary":         map[string]any{"label": change.Primary.Label},
		"after_count":         len(after),
	}
	if active, err := projectroot.ActiveRoot(after, activeRootID); err == nil {
		activeID = active.ID
		data["active_root"] = map[string]any{"id": active.ID, "label": active.Label}
	}
	afterRows := rootsKickRows(after, activeID)
	data["after"] = afterRows
	data["after_omitted_count"] = len(after) - len(afterRows)
	return data
}

func rootsKickRows(roots []projectroot.RootRef, activeID string) []map[string]any {
	limit := len(roots)
	if limit > pongoplain.MaxCollectionItems {
		limit = pongoplain.MaxCollectionItems
	}
	out := make([]map[string]any, 0, limit)
	for _, r := range roots[:limit] {
		out = append(out, map[string]any{
			"id":        r.ID,
			"label":     r.Label,
			"is_active": activeID != "" && r.ID == activeID,
		})
	}
	return out
}

// PostureChangedKickNeeded reports whether a posture change alters whether
// writes outside the project (including temporary and cache folders) ask first.
func PostureChangedKickNeeded(before, after gate.Posture) bool {
	return before.ReviewsConfinedWrites() != after.ReviewsConfinedWrites()
}

// PostureChangedKickData builds the posture-change notice context.
func PostureChangedKickData(after gate.Posture) map[string]any {
	return map[string]any{
		"reviews_confined_writes": after.ReviewsConfinedWrites(),
	}
}
