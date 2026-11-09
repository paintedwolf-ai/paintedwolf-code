package tools

import (
	"path/filepath"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

// RootNotAttachedCode refuses a path or label that named a folder this
// project no longer has attached, or no longer has at that path.
const RootNotAttachedCode = "ROOT_NOT_ATTACHED"

// AttachedFolders lists the call's folders as @label tokens — the session
// folder marked — then the session's virtual folders, for refusals that name
// what the model can use instead. Host paths never appear here.
func AttachedFolders(tctx ToolContext) []string {
	out := make([]string, 0, len(tctx.Source.Roots)+2)
	active, activeErr := projectroot.ActiveRoot(tctx.Source.Roots, tctx.Source.ActiveRootID)
	for _, r := range tctx.Source.Roots {
		token := "@" + r.Label
		if activeErr == nil && r.ID == active.ID {
			token += " (session folder)"
		}
		out = append(out, token)
	}
	if tctx.Host.SessionScratchDir != "" {
		out = append(out, "@"+projectroot.VirtualScratchLabel)
	}
	if tctx.Host.SessionOutputDir != "" {
		out = append(out, "@"+projectroot.VirtualOutputLabel)
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
		"attached_folders": AttachedFolders(tctx),
	}
	if retired.Reason != projectroot.RootDetached {
		data["current_label"] = retired.Successor.Label
	}
	return &toolrejection.ToolReject{Code: RootNotAttachedCode, Data: data}
}

// ValidateRootsCurrent refuses an action whose captured roots include one
// removed or moved after the context was built: its old path no longer
// carries attachment authority, whatever the frozen root list says.
func ValidateRootsCurrent(tctx ToolContext) *toolrejection.ToolReject {
	for _, root := range tctx.Source.Roots {
		if retired, ok := tctx.Source.RootHistory.RetiredSince(root); ok {
			return RootNotAttachedReject(tctx, rootDisplaySpelling(root), retired)
		}
	}
	return nil
}

// rootDisplaySpelling renders a root the way the model addresses it: its
// @label, falling back to the host path only for an unlabeled root.
func rootDisplaySpelling(root projectroot.RootRef) string {
	if root.Label != "" {
		return "@" + root.Label
	}
	return root.Path
}
