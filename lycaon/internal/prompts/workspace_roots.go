package prompts

import (
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/pongoplain"
	"github.com/lycaon/lycaon/internal/projectroot"
)

// MergeWorkspaceRootsVars adds workspace_roots and root_count for workspace-roots.md.
func MergeWorkspaceRootsVars(vars map[string]any, roots []projectroot.RootRef, activePath string) {
	if vars == nil {
		return
	}
	if _, ok := vars["reviews_confined_writes"].(bool); !ok {
		vars["reviews_confined_writes"] = true
	}
	vars["root_count"] = len(roots)
	if activePath != "" {
		vars["project_dir"] = activePath
	}
	if len(roots) == 0 {
		vars["workspace_roots"] = []map[string]any{}
		vars["workspace_roots_omitted_count"] = 0
		return
	}
	limit := len(roots)
	if limit > pongoplain.MaxCollectionItems {
		limit = pongoplain.MaxCollectionItems
	}
	out := make([]map[string]any, 0, limit)
	for _, r := range roots[:limit] {
		out = append(out, map[string]any{
			"label":      r.Label,
			"path":       r.Path,
			"is_primary": r.IsPrimary,
			"is_active":  activePath != "" && r.Path == activePath,
		})
	}
	vars["workspace_roots"] = out
	vars["workspace_roots_omitted_count"] = len(roots) - len(out)
}

// RootsChangedKickData builds the root-change notice context: one row per
// added, removed, moved or renamed root in the change, then the root set the
// next turn resolves against.
func RootsChangedKickData(change projectroot.RootChange, after []projectroot.RootRef, activeRootID string) map[string]any {
	changes := make([]map[string]any, 0, len(change.Added)+len(change.Retired))
	for _, r := range change.Added {
		changes = append(changes, map[string]any{"kind": "added", "label": r.Label, "path": r.Path})
	}
	for _, r := range change.Retired {
		row := map[string]any{"kind": string(r.Reason), "label": r.Root.Label, "old_label": r.Root.Label, "old_path": r.Root.Path}
		if r.Reason != projectroot.RootDetached {
			row["label"], row["path"] = r.Successor.Label, r.Successor.Path
		}
		changes = append(changes, row)
	}
	if len(changes) > pongoplain.MaxCollectionItems {
		changes = changes[:pongoplain.MaxCollectionItems]
	}
	afterRows := rootsKickRows(after)
	data := map[string]any{
		"changes":             changes,
		"changes_omitted":     len(change.Added) + len(change.Retired) - len(changes),
		"generation":          change.Generation,
		"primary_changed":     change.Primary.ID != "",
		"new_primary":         map[string]any{"label": change.Primary.Label, "path": change.Primary.Path},
		"after":               afterRows,
		"after_count":         len(after),
		"after_omitted_count": len(after) - len(afterRows),
	}
	if active, err := projectroot.ActiveRoot(after, activeRootID); err == nil {
		data["active_root"] = map[string]any{"id": active.ID, "label": active.Label, "path": active.Path}
	}
	return data
}

func rootsKickRows(roots []projectroot.RootRef) []map[string]any {
	limit := len(roots)
	if limit > pongoplain.MaxCollectionItems {
		limit = pongoplain.MaxCollectionItems
	}
	out := make([]map[string]any, 0, limit)
	for _, r := range roots[:limit] {
		out = append(out, map[string]any{
			"id":         r.ID,
			"label":      r.Label,
			"path":       r.Path,
			"is_primary": r.IsPrimary,
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
