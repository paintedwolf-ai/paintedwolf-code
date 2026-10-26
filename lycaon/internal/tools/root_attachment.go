package tools

import (
	"path/filepath"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

// RootNotAttachedCode refuses a path or label that named a folder this
// project no longer has attached, or no longer has at that path.
const RootNotAttachedCode = "ROOT_NOT_ATTACHED"

// AttachedFolders lists the call's folders as "label → path" lines, then
// session scratch, for refusals that name what the model can use instead.
func AttachedFolders(tctx ToolContext) []string {
	out := make([]string, 0, len(tctx.Source.Roots)+2)
	for _, r := range tctx.Source.Roots {
		out = append(out, r.Label+" → "+filepath.Clean(r.Path))
	}
	if dir := tctx.Host.SessionScratchDir; dir != "" {
		out = append(out, projectroot.VirtualScratchLabel+" → "+filepath.Clean(dir))
	}
	if dir := tctx.Host.SessionOutputDir; dir != "" {
		out = append(out, projectroot.VirtualOutputLabel+" → "+filepath.Clean(dir))
	}
	return out
}

// RootNotAttachedReject refuses path, which addressed a retired root
// spelling, with the retirement facts and the folders that resolve now.
func RootNotAttachedReject(tctx ToolContext, path string, retired projectroot.Retired) *toolrejection.ToolReject {
	data := map[string]any{
		"path":             filepath.ToSlash(path),
		"root_label":       retired.Root.Label,
		"root_status":      string(retired.Reason),
		"former_path":      filepath.Clean(retired.Root.Path),
		"attached_folders": AttachedFolders(tctx),
	}
	if retired.Reason != projectroot.RootDetached {
		data["current_label"], data["current_path"] = retired.Successor.Label, filepath.Clean(retired.Successor.Path)
	}
	return &toolrejection.ToolReject{Code: RootNotAttachedCode, Data: data}
}

// ValidateRootsCurrent refuses an action whose captured roots include one
// removed or moved after the context was built: its old path no longer
// carries attachment authority, whatever the frozen root list says.
func ValidateRootsCurrent(tctx ToolContext) *toolrejection.ToolReject {
	for _, root := range tctx.Source.Roots {
		if retired, ok := tctx.Source.RootHistory.RetiredSince(root); ok {
			return RootNotAttachedReject(tctx, root.Path, retired)
		}
	}
	return nil
}
