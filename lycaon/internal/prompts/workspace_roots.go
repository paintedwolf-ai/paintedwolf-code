package prompts

import (
	"github.com/lycaon/lycaon/internal/pongoplain"
	"github.com/lycaon/lycaon/internal/projectroot"
)

// MergeWorkspaceRootsVars adds workspace_roots and root_count for workspace-roots.md.
func MergeWorkspaceRootsVars(vars map[string]any, roots []projectroot.RootRef, activePath string) {
	if vars == nil {
		return
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

// RootsChangedKickNeeded reports whether coordinator-roots-changed.md would emit guidance.
func RootsChangedKickNeeded(before, after []projectroot.RootRef) bool {
	added := diffRoots(after, before)
	removed := diffRoots(before, after)
	if len(removed) > 0 {
		return true
	}
	if len(added) > 0 && len(before) <= 1 && len(after) >= 2 {
		return true
	}
	beforePrimary, _ := projectroot.PrimaryRoot(before)
	afterPrimary, _ := projectroot.PrimaryRoot(after)
	primaryChanged := beforePrimary.ID != "" && afterPrimary.ID != "" && beforePrimary.ID != afterPrimary.ID
	if len(before) == 1 && len(after) == 1 && before[0].ID == after[0].ID && before[0].Path != after[0].Path {
		return true
	}
	return primaryChanged && len(after) >= 2
}

// RootsChangedKickData builds kick template context for roots-changed.md.
func RootsChangedKickData(before, after []projectroot.RootRef, activeRootID string) map[string]any {
	added := diffRoots(after, before)
	removed := diffRoots(before, after)
	beforeRows := rootsKickRows(before)
	afterRows := rootsKickRows(after)
	data := map[string]any{
		"before":        beforeRows,
		"before_count":  len(before),
		"after":         afterRows,
		"after_count":   len(after),
		"added_count":   len(added),
		"removed_count": len(removed),
	}
	data["after_omitted_count"] = len(after) - len(afterRows)
	if active, err := projectroot.ActiveRoot(after, activeRootID); err == nil {
		data["active_root"] = map[string]any{"id": active.ID, "label": active.Label, "path": active.Path}
	}
	beforePrimary, _ := projectroot.PrimaryRoot(before)
	afterPrimary, _ := projectroot.PrimaryRoot(after)
	data["primary_changed"] = beforePrimary.ID != "" && afterPrimary.ID != "" && beforePrimary.ID != afterPrimary.ID
	if afterPrimary.ID != "" {
		data["new_primary"] = map[string]any{
			"label":      afterPrimary.Label,
			"path":       afterPrimary.Path,
			"is_primary": afterPrimary.IsPrimary,
		}
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

func diffRoots(want, have []projectroot.RootRef) []projectroot.RootRef {
	haveIDs := make(map[string]struct{}, len(have))
	for _, r := range have {
		haveIDs[r.ID] = struct{}{}
	}
	var out []projectroot.RootRef
	for _, r := range want {
		if _, ok := haveIDs[r.ID]; !ok {
			out = append(out, r)
		}
	}
	return out
}
